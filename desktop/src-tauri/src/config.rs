use serde::{Deserialize, Serialize};
use std::path::PathBuf;

/// Persistent desktop settings. Stored as JSON in the platform config directory.
/// `server_url` and the bearer token are separate: the token lives in the OS keyring
/// (see `auth`), never in this file.
#[derive(Debug, Clone, PartialEq, Serialize, Deserialize)]
#[serde(default)]
pub struct Settings {
    /// Instance origin, e.g. `https://games.example` (no trailing slash).
    pub server_url: String,
    /// Where assembled game files are written.
    pub download_dir: PathBuf,
    /// Concurrent object downloads.
    pub concurrency: usize,
    /// `dark` or `light`.
    pub theme: String,
}

impl Default for Settings {
    fn default() -> Self {
        let download_dir = dirs::download_dir()
            .or_else(dirs::home_dir)
            .map(|h| h.join("GameDepot"))
            .unwrap_or_else(|| PathBuf::from("GameDepot"));
        Settings {
            server_url: String::new(),
            download_dir,
            concurrency: 4,
            theme: "dark".to_string(),
        }
    }
}

impl Settings {
    /// Normalize the server URL: trim whitespace and any trailing slashes. Returns an
    /// error for a value that is not an http(s) origin.
    pub fn normalized_server_url(&self) -> Result<String, String> {
        let s = self.server_url.trim().trim_end_matches('/');
        if !(s.starts_with("http://") || s.starts_with("https://")) {
            return Err("server URL must start with http:// or https://".into());
        }
        Ok(s.to_string())
    }
}

#[derive(Debug, thiserror::Error)]
pub enum ConfigError {
    #[error("io: {0}")]
    Io(#[from] std::io::Error),
    #[error("json: {0}")]
    Json(#[from] serde_json::Error),
}

/// Load settings from a path, falling back to defaults when the file is absent.
pub fn load(path: &std::path::Path) -> Settings {
    match std::fs::read(path) {
        Ok(bytes) => serde_json::from_slice(&bytes).unwrap_or_default(),
        Err(_) => Settings::default(),
    }
}

/// Write settings, creating the parent directory when needed.
pub fn save(path: &std::path::Path, s: &Settings) -> Result<(), ConfigError> {
    if let Some(parent) = path.parent() {
        std::fs::create_dir_all(parent)?;
    }
    let pretty = serde_json::to_vec_pretty(s)?;
    std::fs::write(path, pretty)?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn round_trips_through_disk() {
        let dir = std::env::temp_dir().join(format!("gd-config-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let path = dir.join("settings.json");
        let s = Settings {
            server_url: "https://games.example".into(),
            download_dir: PathBuf::from("/library"),
            concurrency: 8,
            theme: "light".into(),
        };
        save(&path, &s).unwrap();
        let back = load(&path);
        assert_eq!(s, back);
        std::fs::remove_dir_all(&dir).ok();
    }

    #[test]
    fn missing_file_gives_defaults_and_bad_json_falls_back() {
        let dir = std::env::temp_dir().join(format!("gd-config-missing-{}", std::process::id()));
        let s = load(&dir.join("nope.json"));
        assert_eq!(s.concurrency, 4);
        assert_eq!(s.theme, "dark");

        std::fs::create_dir_all(&dir).unwrap();
        let bad = dir.join("settings.json");
        std::fs::write(&bad, b"{ not json").unwrap();
        assert_eq!(load(&bad), Settings::default());
        std::fs::remove_dir_all(&dir).ok();
    }

    #[test]
    fn normalizes_server_url() {
        let mut s = Settings::default();
        s.server_url = "  https://games.example/// ".into();
        assert_eq!(s.normalized_server_url().unwrap(), "https://games.example");
        s.server_url = "games.example".into();
        assert!(s.normalized_server_url().is_err());
    }
}
