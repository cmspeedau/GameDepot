use std::collections::HashMap;
use std::io::{Cursor, Read};
use std::path::Path;

use sha2::{Digest, Sha256};

/// One collected save file. `key` is the path relative to the game's save root; it is
/// what the server and other devices key on, so it must be stable across machines.
#[derive(Debug, Clone, PartialEq)]
pub struct FileEntry {
    pub key: String,
    pub size: i64,
    pub sha256: String,
    pub mode: u32,
}

/// Logical identity of a save set. This MUST match Go `internal/saves.ContentHash`:
/// entries sorted by key, joined as `key\0size\0sha256\0<octal mode>\n` (mode masked to
/// 12 bits, no leading zeros), then SHA-256 hex. A mismatch would make the server see
/// every push as a change and break conflict/noop detection.
pub fn content_hash(entries: &[FileEntry]) -> String {
    let mut sorted: Vec<&FileEntry> = entries.iter().collect();
    sorted.sort_by(|a, b| a.key.cmp(&b.key));
    let mut h = Sha256::new();
    for e in sorted {
        let line = format!("{}\0{}\0{}\0{:o}\n", e.key, e.size, e.sha256, e.mode & 0o7777);
        h.update(line.as_bytes());
    }
    hex::encode(h.finalize())
}

/// Replace `{name}` placeholders from `vars`. Unknown placeholders are left intact so a
/// caller can detect that a required variable (for example `{steamid}`) was not supplied
/// rather than silently expanding to a wrong path.
pub fn expand_template(template: &str, vars: &HashMap<String, String>) -> String {
    let mut out = String::with_capacity(template.len());
    let bytes = template.as_bytes();
    let mut i = 0;
    while i < bytes.len() {
        if bytes[i] == b'{' {
            if let Some(end) = template[i..].find('}') {
                let name = &template[i + 1..i + end];
                match vars.get(name) {
                    Some(v) => out.push_str(v),
                    None => out.push_str(&template[i..i + end + 1]),
                }
                i += end + 1;
                continue;
            }
        }
        out.push(bytes[i] as char);
        i += 1;
    }
    out
}

/// Build a `FileEntry` for one on-disk file under `key`.
pub fn entry_from_file(key: &str, path: &Path) -> Result<FileEntry, String> {
    let mut data = Vec::new();
    std::fs::File::open(path)
        .map_err(|e| format!("open {}: {e}", path.display()))?
        .read_to_end(&mut data)
        .map_err(|e| e.to_string())?;
    let mut h = Sha256::new();
    h.update(&data);
    Ok(FileEntry {
        key: key.to_string(),
        size: data.len() as i64,
        sha256: hex::encode(h.finalize()),
        mode: file_mode(path),
    })
}

#[cfg(unix)]
fn file_mode(path: &Path) -> u32 {
    use std::os::unix::fs::PermissionsExt;
    std::fs::metadata(path).map(|m| m.permissions().mode() & 0o7777).unwrap_or(0o644)
}
#[cfg(not(unix))]
fn file_mode(_path: &Path) -> u32 {
    0o644
}

/// Pack the given (entry, bytes) pairs into a `tar.zstd` archive. Keys are stored as the
/// tar member paths; the entry mode is applied. Content-addressed blobs already exclude
/// duplicates, so each key appears once.
pub fn build_archive(items: &[(FileEntry, Vec<u8>)]) -> Result<Vec<u8>, String> {
    let mut ar = tar::Builder::new(Vec::new());
    for (e, bytes) in items {
        let mut header = tar::Header::new_gnu();
        header.set_size(bytes.len() as u64);
        header.set_mode(e.mode as u32 & 0o7777);
        header.set_cksum();
        ar.append_data(&mut header, &e.key, Cursor::new(bytes))
            .map_err(|err| err.to_string())?;
    }
    let tar = ar.into_inner().map_err(|e| e.to_string())?;
    zstd::encode_all(Cursor::new(tar), 3).map_err(|e| e.to_string())
}

/// Read a `tar.zstd` archive back into (key, mode, bytes) for restore.
pub fn read_archive(bytes: &[u8]) -> Result<Vec<(String, u32, Vec<u8>)>, String> {
    let tar = zstd::decode_all(bytes).map_err(|e| e.to_string())?;
    let mut ar = tar::Archive::new(Cursor::new(tar));
    let mut out = Vec::new();
    for entry in ar.entries().map_err(|e| e.to_string())? {
        let mut entry = entry.map_err(|e| e.to_string())?;
        let path = entry
            .path()
            .map_err(|e| e.to_string())?
            .to_string_lossy()
            .replace('\\', "/");
        let header = entry.header();
        let mode = header.mode().unwrap_or(0o644) as u32 & 0o7777;
        let mut buf = Vec::new();
        entry.read_to_end(&mut buf).map_err(|e| e.to_string())?;
        out.push((path, mode, buf));
    }
    Ok(out)
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::Write;

    #[test]
    fn content_hash_matches_go_vector() {
        // Golden produced by Go internal/saves.ContentHash (cross-language contract).
        let a = vec![
            FileEntry {
                key: "b.cfg".into(),
                size: 11,
                sha256: "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855".into(),
                mode: 0o644,
            },
            FileEntry {
                key: "a/save.dat".into(),
                size: 5,
                sha256: "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824".into(),
                mode: 0o600,
            },
        ];
        let mut b = a.clone();
        b.reverse();
        let expect = "0315f917528121b3d00b918c75fade2fc4366f0518cabadd61e3f2461e486f00";
        assert_eq!(content_hash(&a), expect);
        assert_eq!(content_hash(&b), expect, "must be order-independent");
    }

    #[test]
    fn template_expansion() {
        let mut vars = HashMap::new();
        vars.insert("user".to_string(), "/home/alice".to_string());
        vars.insert("p".to_string(), "Game".to_string());
        assert_eq!(
            expand_template("{user}/.local/share/{p}/saves", &vars),
            "/home/alice/.local/share/Game/saves"
        );
        // Unknown placeholder is left for the caller to detect.
        assert_eq!(expand_template("{steamid}/x", &vars), "{steamid}/x");
    }

    #[test]
    fn archive_roundtrip_preserves_content_and_mode() {
        let entries = vec![
            FileEntry {
                key: "save1.dat".into(),
                size: 5,
                sha256: hex::encode(Sha256::digest(b"hello")),
                mode: 0o640,
            },
            FileEntry {
                key: "dir/save2.dat".into(),
                size: 3,
                sha256: hex::encode(Sha256::digest(b"abc")),
                mode: 0o644,
            },
        ];
        let items = vec![
            (entries[0].clone(), b"hello".to_vec()),
            (entries[1].clone(), b"abc".to_vec()),
        ];
        let archive = build_archive(&items).unwrap();
        let back = read_archive(&archive).unwrap();
        assert_eq!(back.len(), 2);
        for (key, mode, bytes) in &back {
            let e = entries.iter().find(|e| &e.key == key).unwrap();
            assert_eq!(*mode, e.mode & 0o7777);
            assert_eq!(hex::encode(Sha256::digest(bytes)), e.sha256);
        }
    }

    #[test]
    fn entry_from_file_hashes_content() {
        let dir = std::env::temp_dir().join(format!("gd-saves-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let p = dir.join("f.bin");
        let mut f = std::fs::File::create(&p).unwrap();
        f.write_all(b"hello").unwrap();
        let e = entry_from_file("f.bin", &p).unwrap();
        assert_eq!(e.size, 5);
        assert_eq!(e.sha256, hex::encode(Sha256::digest(b"hello")));
        std::fs::remove_dir_all(&dir).ok();
    }
}
