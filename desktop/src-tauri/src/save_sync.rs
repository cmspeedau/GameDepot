

use crate::api::{ApiClient, ApiError};

#[derive(Debug, Clone, PartialEq)]
pub enum SyncOutcome {
    /// Nothing to do: local and server agree.
    Noop { base_seq: i32, content_hash: String },
    /// Local changes were pushed; new version created on the server.
    Pushed { new_seq: i32, content_hash: String },
    /// Server had newer content; the pulled files are ready to be written to disk.
    Pulled {
        new_seq: i32,
        content_hash: String,
        files: Vec<(String, u32, Vec<u8>)>,
    },
    /// Both sides changed since the last sync. No write happened; caller must merge.
    Conflict {
        server_seq: i32,
        server_content_hash: String,
        local_content_hash: String,
    },
}

#[derive(Debug, thiserror::Error)]
pub enum SyncError {
    #[error("api: {0}")]
    Api(#[from] ApiError),
    #[error("decode: {0}")]
    Decode(String),
    #[error("io: {0}")]
    Io(#[from] std::io::Error),
}

/// Client-side 4-way sync decision, mirroring Ludusavi-style "both changed" handling.
/// The server's `DecideSync` only knows about pushes; this adds the pull branch.
pub fn client_decide(
    local_changed: bool,
    server_changed: bool,
) -> &'static str {
    match (local_changed, server_changed) {
        (false, false) => "noop",
        (true, false) => "push",
        (false, true) => "pull",
        (true, true) => "conflict",
    }
}

/// Drive one slot end to end.
///
/// `local_entries` / `local_hash` describe the current on-disk save set; `base_seq` and
/// `last_content_hash` describe what the client last synced to. `archive` is the
/// already-packed `tar.zstd` bytes of the local set (built by the caller from the
/// collected entries). `save_root` is where pulled files are conceptually written; this
/// function returns them in `Pulled.files` rather than writing, so the caller controls
/// placement and conflict UI.
pub async fn sync_slot(
    client: &dyn ApiClient,
    game_id: &str,
    slot: i32,
    base_seq: i32,
    last_content_hash: &str,
    local_hash: &str,
    archive: Vec<u8>,
    file_count: u32,
) -> Result<SyncOutcome, SyncError> {
    let local_changed = if last_content_hash.is_empty() {
        !local_hash.is_empty()
    } else {
        local_hash != last_content_hash
    };

    // Server head.
    let versions = client.list_save_versions(game_id, slot).await?;
    let head = versions.first();
    let (head_seq, head_hash) = match head {
        Some(v) => (v.seq, v.content_hash.clone()),
        None => (0, String::new()),
    };
    let server_changed = head_seq != base_seq || (head_seq != 0 && head_hash != last_content_hash);

    match client_decide(local_changed, server_changed) {
        "noop" => Ok(SyncOutcome::Noop {
            base_seq: head_seq,
            content_hash: if head_hash.is_empty() { local_hash.to_string() } else { head_hash },
        }),
        "push" => {
            match client
                .push_save(game_id, slot, local_hash, base_seq, file_count, &archive)
                .await
            {
                Ok(crate::api::PushOutcome::Version(v)) => Ok(SyncOutcome::Pushed {
                    new_seq: v.seq,
                    content_hash: v.content_hash,
                }),
                Ok(crate::api::PushOutcome::Noop { head_version_id: _ }) => {
                    // Server says already equal; treat as noop at head.
                    Ok(SyncOutcome::Noop {
                        base_seq: head_seq,
                        content_hash: local_hash.to_string(),
                    })
                }
                Err(crate::api::ApiError::Conflict { head_seq, head_version_id: _ }) => {
                    Ok(SyncOutcome::Conflict {
                        server_seq: head_seq,
                        server_content_hash: head_hash,
                        local_content_hash: local_hash.to_string(),
                    })
                }
                Err(e) => Err(SyncError::Api(e)),
            }
        }
        "pull" => {
            // Download the head archive and extract it; client places files.
            let bytes = client.download_save(game_id, slot, Some(head_seq)).await?;
            let files = crate::saves::read_archive(&bytes).map_err(SyncError::Decode)?;
            Ok(SyncOutcome::Pulled {
                new_seq: head_seq,
                content_hash: head_hash.clone(),
                files,
            })
        }
        _ => Ok(SyncOutcome::Conflict {
            server_seq: head_seq,
            server_content_hash: head_hash,
            local_content_hash: local_hash.to_string(),
        }),
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::api::{ApiError, LibraryItem, Me, PairingClaim, PairingInitiate, PushOutcome, ReleaseInfo, SaveManifest, SavePath, SaveSlot, SaveVersion};
    use async_trait::async_trait;
    use std::sync::Mutex;

    fn slot_head(seq: i32, hash: &str) -> SaveVersion {
        SaveVersion {
            id: "v".into(),
            seq,
            kind: "save".into(),
            content_hash: hash.into(),
            archive_sha256: "a".into(),
            size: 1,
            file_count: 1,
            pinned: false,
            created_at: "".into(),
        }
    }

    fn slot() -> SaveSlot {
        SaveSlot {
            id: "s".into(),
            game_id: "g".into(),
            slot_index: 0,
            label: "".into(),
            head_version_id: None,
            created_at: "".into(),
            updated_at: "".into(),
        }
    }

    struct Mock {
        head_seq: i32,
        head_hash: String,
        // canned pull archive
        pull_bytes: Vec<u8>,
        push_result: Mutex<Option<Result<PushOutcome, ApiError>>>,
        push_calls: Mutex<u32>,
    }

    #[async_trait]
    impl ApiClient for Mock {
        async fn initiate_pairing(&self, _n: &str, _p: &str) -> Result<PairingInitiate, ApiError> { unreachable!() }
        async fn claim_pairing(&self, _c: &str) -> Result<PairingClaim, ApiError> { unreachable!() }
        async fn me(&self) -> Result<Me, ApiError> { unreachable!() }
        async fn library(&self) -> Result<Vec<LibraryItem>, ApiError> { unreachable!() }
        async fn release(&self, _v: &str) -> Result<ReleaseInfo, ApiError> { unreachable!() }
        async fn get_manifest(&self, _d: &str) -> Result<Vec<u8>, ApiError> { unreachable!() }
        async fn get_object(&self, _s: &str) -> Result<Vec<u8>, ApiError> { unreachable!() }
        async fn post_playtime(&self, _g: &str, _s: u64) -> Result<(), ApiError> { unreachable!() }
        async fn list_saves(&self, _g: Option<&str>) -> Result<Vec<SaveSlot>, ApiError> { unreachable!() }
        async fn save_manifest(&self, _g: &str) -> Result<SaveManifest, ApiError> { unreachable!() }
        async fn push_save(&self, _g: &str, _s: i32, _h: &str, _b: i32, _f: u32, _a: &[u8]) -> Result<PushOutcome, ApiError> {
            *self.push_calls.lock().unwrap() += 1;
            self.push_result.lock().unwrap().take().unwrap_or(Ok(PushOutcome::Version(SaveVersion {
                id: "new".into(), seq: 99, kind: "save".into(), content_hash: "pushed".into(),
                archive_sha256: "a".into(), size: 1, file_count: 1, pinned: false, created_at: "".into(),
            })))
        }
        async fn download_save(&self, _g: &str, _s: i32, _seq: Option<i32>) -> Result<Vec<u8>, ApiError> {
            Ok(self.pull_bytes.clone())
        }
        async fn list_save_versions(&self, _g: &str, _s: i32) -> Result<Vec<SaveVersion>, ApiError> {
            Ok(vec![slot_head(self.head_seq, &self.head_hash)])
        }
        async fn restore_save(&self, _g: &str, _s: i32, _v: &str) -> Result<SaveVersion, ApiError> { unreachable!() }
        async fn pin_save(&self, _g: &str, _s: i32, _v: &str, _p: bool) -> Result<(), ApiError> { Ok(()) }
        async fn delete_save_version(&self, _g: &str, _s: i32, _v: &str) -> Result<(), ApiError> { Ok(()) }
    }

    #[tokio::test]
    async fn push_when_local_changed_server_stale() {
        let mock = Mock {
            head_seq: 1, head_hash: "old".into(),
            pull_bytes: vec![], push_result: Mutex::new(None),
            push_calls: Mutex::new(0),
        };
        let out = sync_slot(&mock, "g", 0, 1, "old", "new", vec![1, 2, 3], 1).await.unwrap();
        assert!(matches!(out, SyncOutcome::Pushed { new_seq: 99, .. }));
        assert_eq!(*mock.push_calls.lock().unwrap(), 1);
    }

    #[tokio::test]
    async fn pull_when_local_unchanged_server_changed() {
        // build a valid tar.zstd archive in the test to return as pull bytes
        let entries = vec![crate::saves::FileEntry { key: "f.txt".into(), size: 5, sha256: hex_encode(b"hello"), mode: 0o644 }];
        let items = vec![(entries[0].clone(), b"hello".to_vec())];
        let archive = crate::saves::build_archive(&items).unwrap();
        let mock = Mock {
            head_seq: 2, head_hash: "server-new".into(),
            pull_bytes: archive.clone(),
            push_result: Mutex::new(None),
            push_calls: Mutex::new(0),
        };
        let out = sync_slot(&mock, "g", 0, 1, "local", "local", vec![], 0).await.unwrap();
        match out {
            SyncOutcome::Pulled { new_seq, content_hash, files } => {
                assert_eq!(new_seq, 2);
                assert_eq!(content_hash, "server-new");
                assert_eq!(files.len(), 1);
                assert_eq!(files[0].2, b"hello");
            }
            other => panic!("expected Pulled, got {other:?}"),
        }
        assert_eq!(*mock.push_calls.lock().unwrap(), 0);
    }

    #[tokio::test]
    async fn noop_when_both_unchanged() {
        let mock = Mock {
            head_seq: 1, head_hash: "same".into(),
            pull_bytes: vec![], push_result: Mutex::new(None),
            push_calls: Mutex::new(0),
        };
        let out = sync_slot(&mock, "g", 0, 1, "same", "same", vec![], 0).await.unwrap();
        assert!(matches!(out, SyncOutcome::Noop { base_seq: 1, .. }));
        assert_eq!(*mock.push_calls.lock().unwrap(), 0);
    }

    #[tokio::test]
    async fn conflict_when_both_changed() {
        let mock = Mock {
            head_seq: 2, head_hash: "server-new".into(),
            pull_bytes: vec![], push_result: Mutex::new(Some(Err(ApiError::Conflict {
                head_seq: 2, head_version_id: "vh".into(),
            }))),
            push_calls: Mutex::new(0),
        };
        // local changed (local != last) and server changed
        let out = sync_slot(&mock, "g", 0, 1, "old-local", "new-local", vec![], 0).await.unwrap();
        assert!(matches!(out, SyncOutcome::Conflict { server_seq: 2, .. }));
    }

    fn hex_encode(b: &[u8]) -> String {
        const H: &[u8] = b"0123456789abcdef";
        let mut out = String::with_capacity(b.len() * 2);
        for &x in b { out.push(H[(x >> 4) as usize] as char); out.push(H[(x & 15) as usize] as char); }
        out
    }
}
