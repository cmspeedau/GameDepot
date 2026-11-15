use std::path::{Path, PathBuf};

use crate::depot::{
    crypto::{self, DepotError},
    manifest::{FileEntry, Manifest},
};

#[derive(Debug, thiserror::Error)]
pub enum InstallError {
    #[error("unsafe path: {0}")]
    UnsafePath(String),
    #[error("depot: {0}")]
    Depot(#[from] DepotError),
    #[error("io: {0}")]
    Io(String),
}

#[derive(Debug, Default)]
pub struct InstallReport {
    pub files: Vec<String>,
    pub bytes: u64,
}

/// Join `rel` onto `dest`, refusing absolute paths and traversal. The manifest was
/// built with relative, `/`-separated, normalized paths, so anything else is corruption
/// or a hostile manifest and must not be written.
pub fn join_safe(dest: &Path, rel: &str) -> Result<PathBuf, InstallError> {
    if rel.is_empty() || rel.starts_with('/') || rel.contains('\\') {
        return Err(InstallError::UnsafePath(rel.to_string()));
    }
    let mut out = dest.to_path_buf();
    for seg in rel.split('/') {
        if seg.is_empty() || seg == "." || seg == ".." {
            return Err(InstallError::UnsafePath(rel.to_string()));
        }
        out.push(seg);
    }
    Ok(out)
}

/// Assemble every file in `files` into `dest`, decrypting and verifying each through the
/// provided object reader, then set the recorded mode. A partial write never leaves a
/// final path behind: bytes are written to a temp file, fsynced, then renamed.
///
/// `read_object` returns the raw ciphertext for a content hash. It can be backed by a
/// local depot on disk or by a network download; install logic is the same either way.
pub fn install_release<R, E>(
    m: &Manifest,
    dest: &Path,
    files: &[FileEntry],
    read_object: &mut R,
) -> Result<InstallReport, InstallError>
where
    R: FnMut(&str) -> Result<Vec<u8>, E>,
    E: std::fmt::Display,
{
    let mut report = InstallReport::default();
    std::fs::create_dir_all(dest).map_err(|e| InstallError::Io(e.to_string()))?;

    for file in files {
        let bytes = crypto::assemble_file(m, file, |sha| read_object(sha))
            .map_err(InstallError::Depot)?;
        let target = join_safe(dest, &file.path)?;
        write_atomic(&target, &bytes, file.mode)?;
        report.bytes += bytes.len() as u64;
        report.files.push(file.path.clone());
    }
    Ok(report)
}

fn write_atomic(target: &Path, bytes: &[u8], mode: u32) -> Result<(), InstallError> {
    if let Some(parent) = target.parent() {
        std::fs::create_dir_all(parent).map_err(|e| InstallError::Io(e.to_string()))?;
    }
    let tmp = target.with_extension("gdtmp");
    {
        use std::io::Write;
        let mut f = std::fs::File::create(&tmp).map_err(|e| InstallError::Io(e.to_string()))?;
        f.write_all(bytes).map_err(|e| InstallError::Io(e.to_string()))?;
        f.sync_all().map_err(|e| InstallError::Io(e.to_string()))?;
    }
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        std::fs::set_permissions(&tmp, std::fs::Permissions::from_mode(mode & 0o7777))
            .map_err(|e| InstallError::Io(e.to_string()))?;
    }
    #[cfg(not(unix))]
    {
        let _ = mode;
    }
    std::fs::rename(&tmp, target).map_err(|e| InstallError::Io(e.to_string()))?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    fn fixture() -> (PathBuf, crate::depot::FileDepot) {
        let root = Path::new(env!("CARGO_MANIFEST_DIR"))
            .join("fixtures")
            .join("depot");
        let digest = std::fs::read_to_string(root.join("DIGEST")).unwrap();
        let digest = digest.trim();
        let depot = crate::depot::FileDepot::open(&root, digest).unwrap();
        (root, depot)
    }

    #[test]
    fn join_safe_rejects_traversal_and_absolute() {
        assert!(join_safe(Path::new("/dest"), "../evil").is_err());
        assert!(join_safe(Path::new("/dest"), "/etc/passwd").is_err());
        assert!(join_safe(Path::new("/dest"), "a\\b").is_err());
        assert!(join_safe(Path::new("/dest"), "ok/sub/file").unwrap().ends_with("ok/sub/file"));
    }

    #[test]
    fn installs_fixture_files_with_content_and_mode() {
        let (_root, depot) = fixture();
        let dest = std::env::temp_dir().join(format!("gd-install-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dest);

        let mut reader = |sha: &str| depot.read_object(sha);
        let report = install_release(&depot.manifest, &dest, &depot.manifest.files, &mut reader)
            .expect("install");

        assert_eq!(report.files.len(), 2);
        assert_eq!(std::fs::read(dest.join("a.txt")).unwrap(), b"hello world");
        assert_eq!(
            std::fs::read(dest.join("sub/b.txt")).unwrap(),
            b"second file content"
        );

        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            let mode = std::fs::metadata(dest.join("a.txt")).unwrap().permissions().mode();
            assert_eq!(mode & 0o777, 0o644);
        }

        std::fs::remove_dir_all(&dest).ok();
    }
}
