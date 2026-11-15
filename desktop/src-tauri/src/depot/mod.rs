pub mod crypto;
pub mod manifest;

use std::path::{Path, PathBuf};

use crypto::DepotError;
use manifest::Manifest;

/// A depot on disk: the manifest plus a root that holds `objects/<xx>/<sha>`.
pub struct FileDepot {
    pub manifest: Manifest,
    pub root: PathBuf,
}

impl FileDepot {
    /// Load a manifest file by digest and remember the depot root (the directory
    /// that holds `objects/`).
    pub fn open(root: &Path, digest: &str) -> Result<Self, DepotError> {
        let manifest_path = root.join("manifests").join(format!("{digest}.json"));
        let bytes = std::fs::read(&manifest_path)
            .map_err(|e| DepotError::Io(format!("{}: {e}", manifest_path.display())))?;
        let manifest = Manifest::from_json(&bytes)
            .map_err(|e| DepotError::Io(format!("parse manifest: {e}")))?;
        if manifest.digest != digest {
            return Err(DepotError::Io(format!(
                "digest in file {} does not match name {digest}",
                manifest.digest
            )));
        }
        Ok(Self { manifest, root: root.to_path_buf() })
    }

    pub fn read_object(&self, sha_hex: &str) -> Result<Vec<u8>, DepotError> {
        let p = crypto::object_path(&self.root, sha_hex);
        std::fs::read(&p).map_err(|e| DepotError::Io(format!("{}: {e}", p.display())))
    }
}
