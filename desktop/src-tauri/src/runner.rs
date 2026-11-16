use std::path::PathBuf;

use serde::Serialize;

#[derive(Debug, thiserror::Error)]
pub enum RunnerError {
    #[error("io: {0}")]
    Io(String),
    #[error("http: {0}")]
    Http(String),
    #[error("not found: {0}")]
    NotFound(String),
    #[error("verify: {0}")]
    Verify(String),
    #[error("unpack: {0}")]
    Unpack(String),
}

#[derive(Debug, Clone, Serialize)]
pub struct RunnerBuild {
    pub kind: String,
    pub version: String,
    pub path: PathBuf,
    pub source: String,
}

/// Where GameDepot stores runners it acquires itself.
pub fn runner_store_dir() -> PathBuf {
    dirs::data_dir()
        .unwrap_or_else(|| PathBuf::from("."))
        .join("gamedepot")
        .join("runners")
}

/// Where GameDepot stores per-game prefixes.
pub fn prefix_root() -> PathBuf {
    dirs::data_dir()
        .unwrap_or_else(|| PathBuf::from("."))
        .join("gamedepot")
        .join("prefixes")
}

/// Discover umu-run on PATH or in the usual location.
pub fn discover_umu() -> Option<PathBuf> {
    // Explicit common locations first.
    let mut candidates: Vec<PathBuf> = Vec::new();
    if let Some(home) = dirs::home_dir() {
        candidates.push(home.join(".local").join("bin").join("umu-run"));
    }
    candidates.push(PathBuf::from("/usr/bin/umu-run"));
    candidates.push(PathBuf::from("/usr/local/bin/umu-run"));
    for c in &candidates {
        if c.is_file() {
            return Some(c.clone());
        }
    }
    // PATH scan.
    if let Some(path) = std::env::var_os("PATH") {
        for dir in std::env::split_paths(&path) {
            let p = dir.join("umu-run");
            if p.is_file() {
                return Some(p);
            }
        }
    }
    None
}

/// Scan directories where Proton/GE builds are commonly installed and adopt them.
pub fn discover_proton_builds() -> Vec<RunnerBuild> {
    let mut out = Vec::new();
    let mut scan_dirs: Vec<PathBuf> = Vec::new();
    if let Some(home) = dirs::home_dir() {
        scan_dirs.push(home.join(".steam").join("steam").join("compatibilitytools.d"));
        scan_dirs.push(home.join(".local").join("share").join("Steam").join("compatibilitytools.d"));
        scan_dirs.push(home.join(".local").join("share").join("UMU"));
    }
    // Also adopt anything already in our own store.
    scan_dirs.push(runner_store_dir());

    for dir in scan_dirs {
        if !dir.is_dir() {
            continue;
        }
        let rd = match std::fs::read_dir(&dir) {
            Ok(r) => r,
            Err(_) => continue,
        };
        for entry in rd.flatten() {
            let p = entry.path();
            if !p.is_dir() {
                continue;
            }
            // A Proton build contains a `proton` executable (or files/bin/wine).
            let proton = p.join("proton");
            let wine = p.join("files").join("bin").join("wine");
            if !proton.is_file() && !wine.is_file() {
                continue;
            }
            let name = p
                .file_name()
                .map(|s| s.to_string_lossy().to_string())
                .unwrap_or_default();
            out.push(RunnerBuild {
                kind: "proton".to_string(),
                version: name.clone(),
                path: p,
                source: "discovered".to_string(),
            });
        }
    }
    out
}

/// All discovered runners (umu presence is reported separately).
pub fn discover() -> (Option<PathBuf>, Vec<RunnerBuild>) {
    (discover_umu(), discover_proton_builds())
}

/// Fetch metadata for the latest GE-Proton release and pick the asset for this arch.
pub async fn latest_ge_proton() -> Result<(String, String, String), RunnerError> {
    // (tag, tarball_url, sha512_url)
    let client = reqwest::Client::builder()
        .user_agent("GameDepot")
        .build()
        .map_err(|e| RunnerError::Http(e.to_string()))?;
    let resp = client
        .get("https://api.github.com/repos/GloriousEggroll/proton-ge-custom/releases/latest")
        .send()
        .await
        .map_err(|e| RunnerError::Http(e.to_string()))?;
    if !resp.status().is_success() {
        return Err(RunnerError::Http(format!("github: {}", resp.status())));
    }
    let v: serde_json::Value = resp.json().await.map_err(|e| RunnerError::Http(e.to_string()))?;
    let tag = v["tag_name"].as_str().ok_or_else(|| RunnerError::Http("no tag".into()))?.to_string();

    let arch = if cfg!(target_arch = "aarch64") { "aarch64" } else { "x86_64" };
    let tar_name = format!("{tag}-{arch}.tar.gz");
    let sha_name = format!("{tag}-{arch}.sha512sum");

    let mut tar_url = None;
    let mut sha_url = None;
    if let Some(assets) = v["assets"].as_array() {
        for a in assets {
            let name = a["name"].as_str().unwrap_or("");
            if name == tar_name {
                tar_url = a["browser_download_url"].as_str().map(|s| s.to_string());
            } else if name == sha_name {
                sha_url = a["browser_download_url"].as_str().map(|s| s.to_string());
            }
        }
    }
    let tar_url = tar_url.ok_or_else(|| RunnerError::NotFound(tar_name.clone()))?;
    let sha_url = sha_url.ok_or_else(|| RunnerError::NotFound(sha_name.clone()))?;
    Ok((tag, tar_url, sha_url))
}

/// Download, verify (sha512) and unpack the latest GE-Proton into the runner store.
pub async fn install_latest_ge_proton() -> Result<RunnerBuild, RunnerError> {
    let (tag, tar_url, sha_url) = latest_ge_proton().await?;

    let client = reqwest::Client::builder()
        .user_agent("GameDepot")
        .build()
        .map_err(|e| RunnerError::Http(e.to_string()))?;

    // Fetch expected hash.
    let sha_text = client
        .get(&sha_url)
        .send()
        .await
        .map_err(|e| RunnerError::Http(e.to_string()))?
        .text()
        .await
        .map_err(|e| RunnerError::Http(e.to_string()))?;
    let expected = sha_text.split_whitespace().next().ok_or_else(|| RunnerError::Verify("empty sha512".into()))?.to_lowercase();

    // Download tarball to temp file with streaming.
    let store = runner_store_dir();
    std::fs::create_dir_all(&store).map_err(|e| RunnerError::Io(e.to_string()))?;
    let tmp = store.join(format!("{tag}.tar.gz.part"));
    {
        use tokio::io::AsyncWriteExt;
        let mut resp = client.get(&tar_url).send().await.map_err(|e| RunnerError::Http(e.to_string()))?;
        if !resp.status().is_success() {
            return Err(RunnerError::Http(format!("download: {}", resp.status())));
        }
        let mut file = tokio::fs::File::create(&tmp).await.map_err(|e| RunnerError::Io(e.to_string()))?;
        while let Some(chunk) = resp.chunk().await.map_err(|e| RunnerError::Http(e.to_string()))? {
            file.write_all(&chunk).await.map_err(|e| RunnerError::Io(e.to_string()))?;
        }
        file.flush().await.map_err(|e| RunnerError::Io(e.to_string()))?;
    }

    // Verify sha512.
    {
        use sha2::{Digest, Sha512};
        use std::io::Read;
        let mut f = std::fs::File::open(&tmp).map_err(|e| RunnerError::Io(e.to_string()))?;
        let mut hasher = Sha512::new();
        let mut buf = [0u8; 65536];
        loop {
            let n = f.read(&mut buf).map_err(|e| RunnerError::Io(e.to_string()))?;
            if n == 0 { break; }
            hasher.update(&buf[..n]);
        }
        let got = hex::encode(hasher.finalize());
        if got != expected {
            let _ = std::fs::remove_file(&tmp);
            return Err(RunnerError::Verify(format!("sha512 mismatch: got {got}, want {expected}")));
        }
    }

    // Unpack.
    let dest = store.join(&tag);
    if dest.exists() {
        let _ = std::fs::remove_dir_all(&dest);
    }
    std::fs::create_dir_all(&dest).map_err(|e| RunnerError::Io(e.to_string()))?;
    {
        use flate2::read::GzDecoder;
        use std::fs::File;
        let f = File::open(&tmp).map_err(|e| RunnerError::Io(e.to_string()))?;
        let gz = GzDecoder::new(f);
        let mut archive = tar::Archive::new(gz);
        archive.unpack(&store).map_err(|e| RunnerError::Unpack(e.to_string()))?;
    }
    let _ = std::fs::remove_file(&tmp);

    // The archive unpacks into store/<tag> already; if not, adjust.
    let final_path = if store.join(&tag).is_dir() {
        store.join(&tag)
    } else {
        dest
    };

    Ok(RunnerBuild {
        kind: "proton".to_string(),
        version: tag,
        path: final_path,
        source: "downloaded".to_string(),
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn runner_store_is_under_data_dir() {
        let p = runner_store_dir();
        assert!(p.ends_with("gamedepot/runners"));
    }

    #[test]
    fn prefix_root_is_under_data_dir() {
        let p = prefix_root();
        assert!(p.ends_with("gamedepot/prefixes"));
    }

    #[test]
    fn discover_does_not_panic() {
        let (umu, builds) = discover();
        let _ = (umu, builds);
    }
}