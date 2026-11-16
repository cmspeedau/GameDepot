use std::path::{Path, PathBuf};

use serde::Serialize;

#[derive(Debug, thiserror::Error)]
pub enum PrefixError {
    #[error("io: {0}")]
    Io(String),
    #[error("unsafe path: {0}")]
    Unsafe(String),
}

#[derive(Debug, Clone, Serialize)]
pub struct PrefixInfo {
    pub id: String,
    pub game_id: String,
    pub profile: String,
    pub path: PathBuf,
    pub size_bytes: u64,
}

/// Build the isolated prefix path for a game/profile. Refuses path traversal in profile.
pub fn prefix_path(game_id: &str, profile: &str) -> Result<PathBuf, PrefixError> {
    if game_id.is_empty() || profile.is_empty() {
        return Err(PrefixError::Unsafe("empty id or profile".into()));
    }
    if game_id.contains('/') || game_id.contains('\\') || game_id.contains("..")
        || profile.contains('/') || profile.contains('\\') || profile.contains("..")
    {
        return Err(PrefixError::Unsafe(format!("{game_id}/{profile}")));
    }
    Ok(prefix_root().join(format!("{game_id}-{profile}")))
}

fn prefix_root() -> PathBuf {
    crate::runner::prefix_root()
}

/// Best-effort size of a prefix directory (sum of file sizes).
pub fn prefix_size(path: &Path) -> u64 {
    let mut total = 0u64;
    let mut stack = vec![path.to_path_buf()];
    while let Some(dir) = stack.pop() {
        let rd = match std::fs::read_dir(&dir) {
            Ok(r) => r,
            Err(_) => continue,
        };
        for entry in rd.flatten() {
            let p = entry.path();
            if let Ok(ft) = entry.file_type() {
                if ft.is_dir() && !ft.is_symlink() {
                    stack.push(p);
                } else if ft.is_file() {
                    if let Ok(m) = std::fs::metadata(&p) {
                        total += m.len();
                    }
                }
            }
        }
    }
    total
}

/// Delete a prefix directory. Caller is responsible for save backup beforehand.
pub fn delete_prefix(path: &Path) -> Result<(), PrefixError> {
    // Only allow deleting under the prefix root.
    let root = prefix_root();
    let canon_root = root.canonicalize().unwrap_or(root.clone());
    let canon_path = path.canonicalize().unwrap_or_else(|_| path.to_path_buf());
    if !canon_path.starts_with(&canon_root) {
        return Err(PrefixError::Unsafe(format!("not under prefix root: {}", path.display())));
    }
    std::fs::remove_dir_all(path).map_err(|e| PrefixError::Io(e.to_string()))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn prefix_path_rejects_traversal() {
        assert!(prefix_path("../x", "p").is_err());
        assert!(prefix_path("g", "../p").is_err());
        assert!(prefix_path("g", "p").unwrap().ends_with("g-p"));
    }

    #[test]
    fn delete_refuses_escape() {
        let tmp = std::env::temp_dir().join("not-a-prefix");
        let _ = std::fs::create_dir_all(&tmp);
        assert!(delete_prefix(&tmp).is_err());
        let _ = std::fs::remove_dir_all(&tmp);
    }

    #[test]
    fn size_counts_files() {
        let dir = std::env::temp_dir().join(format!("gd-prefix-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir).unwrap();
        std::fs::write(dir.join("a"), b"hello").unwrap();
        assert_eq!(prefix_size(&dir), 5);
        std::fs::remove_dir_all(&dir).ok();
    }
}