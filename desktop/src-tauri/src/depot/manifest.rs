use serde::Deserialize;
use std::collections::HashMap;

/// Mirror of the Go `gamedepot-release` v1 manifest, field for field.
#[derive(Debug, Clone, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct Manifest {
    pub format: String,
    pub version: u32,
    pub chunk_size: u64,
    pub total_size: u64,
    /// Base64 (std, padded) 32-byte AES-256 key.
    pub key: String,
    pub files: Vec<FileEntry>,
    pub objects: HashMap<String, ObjectMeta>,
    pub digest: String,
}

#[derive(Debug, Clone, Deserialize)]
pub struct FileEntry {
    pub path: String,
    pub size: u64,
    pub sha256: String,
    pub mode: u32,
    pub chunks: Vec<ChunkRef>,
}

#[derive(Debug, Clone, Deserialize)]
pub struct ChunkRef {
    pub sha256: String,
    pub offset: u64,
    pub size: u64,
}

#[derive(Debug, Clone, Deserialize)]
#[serde(rename_all = "camelCase")]
pub struct ObjectMeta {
    /// Plaintext size.
    pub size: u64,
    /// Ciphertext size on disk (plaintext + 16 byte tag).
    pub stored_size: i64,
    /// Base64 (std, padded) 12-byte nonce.
    pub nonce: String,
}

impl Manifest {
    pub fn from_json(bytes: &[u8]) -> Result<Self, serde_json::Error> {
        serde_json::from_slice(bytes)
    }
}
