use aes_gcm::aead::{Aead, Payload};
use aes_gcm::{Aes256Gcm, Key, KeyInit, Nonce};
use base64::Engine;
use sha2::{Digest, Sha256};

use super::manifest::{FileEntry, Manifest, ObjectMeta};

#[derive(Debug, thiserror::Error)]
pub enum DepotError {
    #[error("bad base64: {0}")]
    Base64(#[from] base64::DecodeError),
    #[error("bad hex: {0}")]
    Hex(#[from] hex::FromHexError),
    #[error("decrypt failed for object {0}")]
    Decrypt(String),
    #[error("content hash mismatch for object {0}")]
    HashMismatch(String),
    #[error("object {0} not in manifest")]
    MissingObject(String),
    #[error("io: {0}")]
    Io(String),
}

/// AAD binds a chunk to its identity, exactly as the Go server does:
/// `gamedepot-release:v1:<lowercase-hex-sha256>`.
pub fn aad_for(sha_hex: &str) -> Vec<u8> {
    let mut a = b"gamedepot-release:v1:".to_vec();
    a.extend_from_slice(sha_hex.as_bytes());
    a
}

pub fn key_bytes(m: &Manifest) -> Result<[u8; 32], DepotError> {
    let raw = base64::engine::general_purpose::STANDARD.decode(&m.key)?;
    if raw.len() != 32 {
        return Err(DepotError::Io(format!("key length {}", raw.len())));
    }
    let mut k = [0u8; 32];
    k.copy_from_slice(&raw);
    Ok(k)
}

/// Decrypt a single object's ciphertext to plaintext, verifying the plaintext hash.
pub fn decrypt_object(
    key: &[u8; 32],
    sha_hex: &str,
    meta: &ObjectMeta,
    ciphertext: &[u8],
) -> Result<Vec<u8>, DepotError> {
    let nonce_raw = base64::engine::general_purpose::STANDARD.decode(&meta.nonce)?;
    if nonce_raw.len() != 12 {
        return Err(DepotError::Io(format!("nonce length {}", nonce_raw.len())));
    }
    let cipher = Aes256Gcm::new(Key::<Aes256Gcm>::from_slice(key));
    let nonce = Nonce::from_slice(&nonce_raw);
    let pt = cipher
        .decrypt(
            nonce,
            Payload {
                msg: ciphertext,
                aad: &aad_for(sha_hex),
            },
        )
        .map_err(|_| DepotError::Decrypt(sha_hex.to_string()))?;

    let mut h = Sha256::new();
    h.update(&pt);
    if hex::encode(h.finalize()) != sha_hex {
        return Err(DepotError::HashMismatch(sha_hex.to_string()));
    }
    if pt.len() as u64 != meta.size {
        return Err(DepotError::Io(format!(
            "plaintext size mismatch for {sha_hex}: got {}, want {}",
            pt.len(),
            meta.size
        )));
    }
    Ok(pt)
}

/// Assemble a whole file by decrypting each referenced object and copying it to its
/// offset. `read_object` supplies the raw ciphertext bytes for a given object hash.
pub fn assemble_file<R, E>(
    m: &Manifest,
    file: &FileEntry,
    mut read_object: R,
) -> Result<Vec<u8>, DepotError>
where
    R: FnMut(&str) -> Result<Vec<u8>, E>,
    E: std::fmt::Display,
{
    let key = key_bytes(m)?;
    let mut out = vec![0u8; file.size as usize];
    for chunk in &file.chunks {
        let meta = m
            .objects
            .get(&chunk.sha256)
            .ok_or_else(|| DepotError::MissingObject(chunk.sha256.clone()))?;
        let ct = read_object(&chunk.sha256)
            .map_err(|e| DepotError::Io(format!("{}: {e}", chunk.sha256)))?;
        let pt = decrypt_object(&key, &chunk.sha256, meta, &ct)?;
        let start = chunk.offset as usize;
        let end = start + pt.len();
        if end > out.len() {
            return Err(DepotError::Io(format!(
                "chunk {} overruns file {}",
                chunk.sha256, file.path
            )));
        }
        out[start..end].copy_from_slice(&pt);
    }

    let mut h = Sha256::new();
    h.update(&out);
    if hex::encode(h.finalize()) != file.sha256 {
        return Err(DepotError::HashMismatch(file.path.clone()));
    }
    Ok(out)
}

/// Object path under a depot root: `objects/<first two hex>/<sha256>`.
pub fn object_path(depot_root: &std::path::Path, sha_hex: &str) -> std::path::PathBuf {
    depot_root.join("objects").join(&sha_hex[..2]).join(sha_hex)
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::depot::manifest::{ChunkRef, ObjectMeta};
    use aes_gcm::aead::{Aead, KeyInit};
    use std::collections::HashMap;

    fn enc(key: &[u8; 32], nonce12: &[u8], pt: &[u8], sha_hex: &str) -> Vec<u8> {
        let cipher = Aes256Gcm::new_from_slice(key).unwrap();
        cipher
            .encrypt(
                Nonce::from_slice(nonce12),
                Payload {
                    msg: pt,
                    aad: &aad_for(sha_hex),
                },
            )
            .unwrap()
    }

    #[test]
    fn multi_chunk_assemble_with_two_objects() {
        let key = [7u8; 32];
        let whole = b"ABCD".to_vec();
        let (c0, c1) = (&whole[..2], &whole[2..]);

        let mut objects = HashMap::new();
        let mut chunks = Vec::new();
        let mut store: HashMap<String, Vec<u8>> = HashMap::new();
        let b64 = base64::engine::general_purpose::STANDARD;

        for (i, (pt, offset)) in [(c0, 0u64), (c1, 2u64)].into_iter().enumerate() {
            let mut h = Sha256::new();
            h.update(pt);
            let sha_hex = hex::encode(h.finalize());
            let nonce = [i as u8 + 1; 12];
            let ct = enc(&key, &nonce, pt, &sha_hex);
            objects.insert(
                sha_hex.clone(),
                ObjectMeta {
                    size: pt.len() as u64,
                    stored_size: ct.len() as i64,
                    nonce: b64.encode(nonce),
                },
            );
            chunks.push(ChunkRef {
                sha256: sha_hex.clone(),
                offset,
                size: pt.len() as u64,
            });
            store.insert(sha_hex, ct);
        }

        let mut fh = Sha256::new();
        fh.update(&whole);
        let file = FileEntry {
            path: "f".into(),
            size: whole.len() as u64,
            sha256: hex::encode(fh.finalize()),
            mode: 0o644,
            chunks,
        };
        let m = Manifest {
            format: "gamedepot-release".into(),
            version: 1,
            chunk_size: 4 * 1024 * 1024,
            total_size: whole.len() as u64,
            key: b64.encode(key),
            files: vec![file.clone()],
            objects,
            digest: "unused".into(),
        };

        let out = assemble_file(&m, &file, |sha| {
            store.get(sha).cloned().ok_or_else(|| "missing".to_string())
        })
        .unwrap();
        assert_eq!(out, whole);
    }
}
