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

/// Object hashes a client must download to assemble the given files, given the set of
/// object hashes it already holds and has verified. This is the incremental-install
/// primitive: updating version A to B fetches only chunks that are new or changed, the
/// same idea Steam's depot reuse relies on. Returns sorted, deduplicated hashes.
pub fn missing_objects<'a, I>(
    files: I,
    have: &std::collections::HashSet<String>,
) -> Vec<String>
where
    I: IntoIterator<Item = &'a crate::depot::manifest::FileEntry>,
{
    let mut out = std::collections::BTreeSet::new();
    for f in files {
        for c in &f.chunks {
            if !have.contains(&c.sha256) {
                out.insert(c.sha256.clone());
            }
        }
    }
    out.into_iter().collect()
}

/// All distinct object hashes referenced by a set of files.
pub fn referenced_objects<'a, I>(m: &Manifest, files: I) -> Vec<String>
where
    I: IntoIterator<Item = &'a crate::depot::manifest::FileEntry>,
{
    let mut out = std::collections::BTreeSet::new();
    for f in files {
        for c in &f.chunks {
            if m.objects.contains_key(&c.sha256) {
                out.insert(c.sha256.clone());
            }
        }
    }
    out.into_iter().collect()
}
