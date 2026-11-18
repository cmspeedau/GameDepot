

use crate::api::{ApiClient, ApiError};
use crate::saves::{self, SavePathTemplate, SaveRoot};
use crate::store::Store;
use serde::Serialize;
use std::collections::HashMap;

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
    #[error("config: {0}")]
    Config(String),
    #[error("store: {0}")]
    Store(#[from] crate::store::StoreError),
}

/// Serializable summary of a sync for the UI. Bytes and file lists stay on the Rust
/// side; the frontend only needs the decision, the new head and counts.
#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "camelCase", tag = "kind")]
pub enum SyncSummary {
    Noop {
        seq: i32,
        content_hash: String,
    },
    Pushed {
        seq: i32,
        content_hash: String,
        file_count: u32,
    },
    Pulled {
        seq: i32,
        content_hash: String,
        files_written: usize,
    },
    Conflict {
        server_seq: i32,
        server_content_hash: String,
        local_content_hash: String,
    },
}

/// Current head version of a slot. A 404 means the slot does not exist yet on the
/// server, which is the same as "no head" for the sync decision.
pub async fn fetch_head(
    client: &dyn ApiClient,
    game_id: &str,
    slot: i32,
) -> Result<(i32, String), SyncError> {
    match client.list_save_versions(game_id, slot).await {
        Ok(versions) => Ok(versions
            .first()
            .map(|v| (v.seq, v.content_hash.clone()))
            .unwrap_or((0, String::new()))),
        Err(ApiError::Api { status: 404, .. }) => Ok((0, String::new())),
        Err(e) => Err(SyncError::Api(e)),
    }
}

/// Expand a game's server save manifest into local roots.
pub fn roots_for(
    manifest: &crate::api::SaveManifest,
    current_platform: &str,
    vars: &HashMap<String, String>,
) -> Result<Vec<SaveRoot>, SyncError> {
    let templates: Vec<SavePathTemplate> = manifest
        .paths
        .iter()
        .map(|p| SavePathTemplate {
            platform: p.platform.clone(),
            kind: p.kind.clone(),
            template: p.template.clone(),
            recursive: p.recursive,
        })
        .collect();
    let roots = saves::resolve_roots(&templates, current_platform, vars).map_err(SyncError::Config)?;
    if roots.is_empty() {
        return Err(SyncError::Config(format!(
            "no file save paths configured for platform {current_platform}"
        )));
    }
    Ok(roots)
}

/// Fetch the manifest and game name for `game_id` and resolve roots with the local
/// template variables. `{p}`/`{name}` use the game's display name from the library,
/// `{user}` the home directory and `{appdata}` the platform config directory.
pub async fn resolve_roots(client: &dyn ApiClient, game_id: &str) -> Result<Vec<SaveRoot>, SyncError> {
    let manifest = client.save_manifest(game_id).await?;
    let library = client.library().await?;
    let name = library
        .iter()
        .find(|g| g.id == game_id)
        .map(|g| g.name.clone())
        .unwrap_or_default();
    let home = dirs::home_dir().ok_or_else(|| SyncError::Config("no home directory".into()))?;
    let appdata = dirs::config_dir().ok_or_else(|| SyncError::Config("no config directory".into()))?;
    let vars = saves::default_vars(&name, &home, &appdata);
    roots_for(&manifest, std::env::consts::OS, &vars)
}

/// Hash identity of a collected set: empty on disk means no content (""), which keeps
/// "never synced" and "synced an empty set" distinguishable from "saved files exist".
fn local_identity(items: &[(saves::FileEntry, Vec<u8>)]) -> (String, u32) {
    let entries: Vec<saves::FileEntry> = items.iter().map(|(e, _)| e.clone()).collect();
    let hash = if entries.is_empty() {
        String::new()
    } else {
        saves::content_hash(&entries)
    };
    (hash, entries.len() as u32)
}

/// Full sync of one slot: collect local saves, compare with the server head, push,
/// pull (writing files back to their roots) or report a conflict. The sync cursor
/// (base sequence + content hash) is persisted in the local store so the next run can
/// tell "both changed" from a one-sided change.
pub async fn sync_game(
    client: &dyn ApiClient,
    store: &Store,
    game_id: &str,
    slot: i32,
    roots: &[SaveRoot],
) -> Result<SyncSummary, SyncError> {
    let items = saves::collect_items(roots).map_err(SyncError::Decode)?;
    let (local_hash, file_count) = local_identity(&items);
    let archive = if local_hash.is_empty() {
        Vec::new()
    } else {
        saves::build_archive(&items).map_err(SyncError::Decode)?
    };

    let (base_seq, last_hash) = store.save_cursor(game_id, slot)?;
    let outcome = sync_slot(
        client, game_id, slot, base_seq, &last_hash, &local_hash, archive, file_count,
    )
    .await?;

    match outcome {
        SyncOutcome::Noop { base_seq, content_hash } => {
            store.set_save_cursor(game_id, slot, base_seq, &content_hash)?;
            Ok(SyncSummary::Noop {
                seq: base_seq,
                content_hash,
            })
        }
        SyncOutcome::Pushed { new_seq, content_hash } => {
            store.set_save_cursor(game_id, slot, new_seq, &content_hash)?;
            Ok(SyncSummary::Pushed {
                seq: new_seq,
                content_hash,
                file_count,
            })
        }
        SyncOutcome::Pulled {
            new_seq,
            content_hash,
            files,
        } => {
            let written = saves::write_pulled(roots, &files).map_err(SyncError::Decode)?;
            store.set_save_cursor(game_id, slot, new_seq, &content_hash)?;
            Ok(SyncSummary::Pulled {
                seq: new_seq,
                content_hash,
                files_written: written,
            })
        }
        SyncOutcome::Conflict {
            server_seq,
            server_content_hash,
            local_content_hash,
        } => Ok(SyncSummary::Conflict {
            server_seq,
            server_content_hash,
            local_content_hash,
        }),
    }
}

/// Resolve a conflict in favour of the server: download the head archive, write it
/// over the local saves and adopt the head as the new cursor.
pub async fn take_server(
    client: &dyn ApiClient,
    store: &Store,
    game_id: &str,
    slot: i32,
    roots: &[SaveRoot],
) -> Result<SyncSummary, SyncError> {
    let (head_seq, head_hash) = fetch_head(client, game_id, slot).await?;
    if head_seq == 0 {
        return Err(SyncError::Config("server has no version for this slot".into()));
    }
    let bytes = client.download_save(game_id, slot, Some(head_seq)).await?;
    let files = saves::read_archive(&bytes).map_err(SyncError::Decode)?;
    let written = saves::write_pulled(roots, &files).map_err(SyncError::Decode)?;
    store.set_save_cursor(game_id, slot, head_seq, &head_hash)?;
    Ok(SyncSummary::Pulled {
        seq: head_seq,
        content_hash: head_hash,
        files_written: written,
    })
}

/// Resolve a conflict in favour of this machine: push the current local set against
/// the server head, making it the newest version. Local changes win; the previous
/// server head stays in history (retention keeps it until pruned).
pub async fn take_local(
    client: &dyn ApiClient,
    store: &Store,
    game_id: &str,
    slot: i32,
    roots: &[SaveRoot],
) -> Result<SyncSummary, SyncError> {
    let items = saves::collect_items(roots).map_err(SyncError::Decode)?;
    let (local_hash, file_count) = local_identity(&items);
    if local_hash.is_empty() {
        return Err(SyncError::Config("no local saves to push".into()));
    }
    let archive = saves::build_archive(&items).map_err(SyncError::Decode)?;
    let (head_seq, _) = fetch_head(client, game_id, slot).await?;
    match client
        .push_save(game_id, slot, &local_hash, head_seq, file_count, &archive)
        .await
    {
        Ok(crate::api::PushOutcome::Version(v)) => {
            store.set_save_cursor(game_id, slot, v.seq, &v.content_hash)?;
            Ok(SyncSummary::Pushed {
                seq: v.seq,
                content_hash: v.content_hash,
                file_count,
            })
        }
        Ok(crate::api::PushOutcome::Noop { head_version_id: _ }) => Ok(SyncSummary::Noop {
            seq: head_seq,
            content_hash: local_hash,
        }),
        Err(ApiError::Conflict { head_seq, head_version_id: _ }) => Ok(SyncSummary::Conflict {
            server_seq: head_seq,
            server_content_hash: String::new(),
            local_content_hash: local_hash,
        }),
        Err(e) => Err(SyncError::Api(e)),
    }
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
    let (head_seq, head_hash) = fetch_head(client, game_id, slot).await?;
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

    // ---- orchestration tests ----

    use crate::saves::{self, SaveRoot};
    use crate::store::Store;
    use sha2::Digest;

    fn mk_version(seq: i32, hash: &str) -> SaveVersion {
        SaveVersion {
            id: format!("v{seq}"),
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

    /// A server that keeps one slot head in memory.
    struct Fake {
        head: Mutex<Option<(i32, String)>>, // (seq, hash)
        archive: Mutex<Vec<u8>>,
        versions: Mutex<Vec<SaveVersion>>,
        download_calls: Mutex<u32>,
    }

    impl Fake {
        fn empty() -> Self {
            Self {
                head: Mutex::new(None),
                archive: Mutex::new(Vec::new()),
                versions: Mutex::new(Vec::new()),
                download_calls: Mutex::new(0),
            }
        }
        fn with_server_version(seq: i32, hash: &str, archive: Vec<u8>) -> Self {
            let f = Self {
                head: Mutex::new(Some((seq, hash.into()))),
                archive: Mutex::new(archive),
                versions: Mutex::new(vec![mk_version(seq, hash)]),
                download_calls: Mutex::new(0),
            };
            f
        }
    }

    #[async_trait]
    impl ApiClient for Fake {
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
        async fn push_save(&self, _g: &str, _s: i32, h: &str, base: i32, _f: u32, archive: &[u8]) -> Result<PushOutcome, ApiError> {
            let head = self.head.lock().unwrap().clone();
            let (head_seq, head_hash) = match &head {
                Some((s, hash)) => (*s, hash.clone()),
                None => (0, String::new()),
            };
            // optimistic concurrency like the server
            if head_seq != base && !head_hash.is_empty() && h != head_hash {
                return Err(ApiError::Conflict { head_seq, head_version_id: format!("v{head_seq}") });
            }
            if h == head_hash {
                return Ok(PushOutcome::Noop { head_version_id: format!("v{head_seq}") });
            }
            let new = mk_version(head_seq + 1, h);
            *self.head.lock().unwrap() = Some((new.seq, new.content_hash.clone()));
            *self.archive.lock().unwrap() = archive.to_vec();
            self.versions.lock().unwrap().insert(0, new.clone());
            Ok(PushOutcome::Version(new))
        }
        async fn download_save(&self, _g: &str, _s: i32, _seq: Option<i32>) -> Result<Vec<u8>, ApiError> {
            *self.download_calls.lock().unwrap() += 1;
            Ok(self.archive.lock().unwrap().clone())
        }
        async fn list_save_versions(&self, _g: &str, _s: i32) -> Result<Vec<SaveVersion>, ApiError> {
            Ok(self.versions.lock().unwrap().clone())
        }
        async fn restore_save(&self, _g: &str, _s: i32, _v: &str) -> Result<SaveVersion, ApiError> { unreachable!() }
        async fn pin_save(&self, _g: &str, _s: i32, _v: &str, _p: bool) -> Result<(), ApiError> { Ok(()) }
        async fn delete_save_version(&self, _g: &str, _s: i32, _v: &str) -> Result<(), ApiError> { Ok(()) }
    }

    fn temp_roots(dir: &std::path::Path) -> Vec<SaveRoot> {
        vec![SaveRoot { label: "root".into(), base: dir.to_path_buf(), recursive: true }]
    }

    #[tokio::test]
    async fn sync_game_pushes_local_then_noop_on_repeat() {
        let dir = std::env::temp_dir().join(format!("gd-sync-push-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir).unwrap();
        std::fs::write(dir.join("save1.dat"), b"one").unwrap();

        let fake = Fake::empty();
        let store = Store::in_memory().unwrap();
        let roots = temp_roots(&dir);

        let summary = sync_game(&fake, &store, "g", 0, &roots).await.unwrap();
        match summary {
            SyncSummary::Pushed { seq, file_count, .. } => {
                assert_eq!(seq, 1);
                assert_eq!(file_count, 1);
            }
            other => panic!("expected pushed, got {other:?}"),
        }
        // cursor persisted
        let (base, _) = store.save_cursor("g", 0).unwrap();
        assert_eq!(base, 1);

        // second sync: nothing changed -> noop, no second push
        let summary2 = sync_game(&fake, &store, "g", 0, &roots).await.unwrap();
        assert!(matches!(summary2, SyncSummary::Noop { seq: 1, .. }), "{summary2:?}");

        // local change -> push again
        std::fs::write(dir.join("save2.dat"), b"two").unwrap();
        let summary3 = sync_game(&fake, &store, "g", 0, &roots).await.unwrap();
        assert!(matches!(summary3, SyncSummary::Pushed { seq: 2, .. }), "{summary3:?}");

        let _ = std::fs::remove_dir_all(&dir);
    }

    #[tokio::test]
    async fn sync_game_pulls_when_only_server_changed() {
        let dir = std::env::temp_dir().join(format!("gd-sync-pull-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir).unwrap();
        std::fs::write(dir.join("s.dat"), b"v1").unwrap();

        // push v1 so the cursor exists
        let fake = Fake::empty();
        let store = Store::in_memory().unwrap();
        let roots = temp_roots(&dir);
        sync_game(&fake, &store, "g", 0, &roots).await.unwrap();

        // server moves to a new archive, local untouched. Simulate by writing a v2 archive
        // the fake will serve and setting head directly.
        let items = vec![(
            saves::FileEntry {
                key: "s.dat".into(),
                size: 2,
                sha256: hex::encode(sha2::Sha256::digest(b"v2")),
                mode: 0o644,
            },
            b"v2".to_vec(),
        )];
        let hash = saves::content_hash(&[items[0].0.clone()]);
        let archive = saves::build_archive(&items).unwrap();
        *fake.head.lock().unwrap() = Some((2, hash.clone()));
        *fake.archive.lock().unwrap() = archive.clone();
        fake.versions.lock().unwrap().insert(0, mk_version(2, &hash));
        // local still holds the v1 bytes and the cursor says v1: only the server moved.
        // A server-only change must pull and overwrite s.dat with the v2 bytes.

        let summary = sync_game(&fake, &store, "g", 0, &roots).await.unwrap();
        match summary {
            SyncSummary::Pulled { seq, files_written, .. } => {
                assert_eq!(seq, 2);
                assert_eq!(files_written, 1);
            }
            other => panic!("expected pulled, got {other:?}"),
        }
        assert_eq!(std::fs::read(dir.join("s.dat")).unwrap(), b"v2");
        let (base, cur_hash) = store.save_cursor("g", 0).unwrap();
        assert_eq!((base, cur_hash), (2, hash));

        let _ = std::fs::remove_dir_all(&dir);
    }

    #[tokio::test]
    async fn sync_game_reports_conflict_when_both_changed() {
        let dir = std::env::temp_dir().join(format!("gd-sync-conflict-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&dir);
        std::fs::create_dir_all(&dir).unwrap();
        std::fs::write(dir.join("s.dat"), b"local-new").unwrap();

        let archive_items = vec![(
            saves::FileEntry {
                key: "s.dat".into(),
                size: 3,
                sha256: hex::encode(sha2::Sha256::digest(b"srv")),
                mode: 0o644,
            },
            b"srv".to_vec(),
        )];
        let srv_hash = saves::content_hash(&[archive_items[0].0.clone()]);
        let fake = Fake::with_server_version(2, &srv_hash, saves::build_archive(&archive_items).unwrap());
        let store = Store::in_memory().unwrap();
        // cursor: synced at v1 with a different hash -> local changed too
        store.set_save_cursor("g", 0, 1, "old-local").unwrap();

        let roots = temp_roots(&dir);
        let summary = sync_game(&fake, &store, "g", 0, &roots).await.unwrap();
        match summary {
            SyncSummary::Conflict { server_seq, .. } => assert_eq!(server_seq, 2),
            other => panic!("expected conflict, got {other:?}"),
        }
        // cursor untouched on conflict
        let (base, _) = store.save_cursor("g", 0).unwrap();
        assert_eq!(base, 1);
        // local file untouched
        assert_eq!(std::fs::read(dir.join("s.dat")).unwrap(), b"local-new");

        // take server wins: local overwritten + cursor adopted
        let taken = take_server(&fake, &store, "g", 0, &roots).await.unwrap();
        assert!(matches!(taken, SyncSummary::Pulled { seq: 2, files_written: 1, .. }), "{taken:?}");
        assert_eq!(std::fs::read(dir.join("s.dat")).unwrap(), b"srv");
        let (base, _) = store.save_cursor("g", 0).unwrap();
        assert_eq!(base, 2);

        // Another device moves the server again while this machine also changes its
        // saves: both changed -> conflict. take_local must then push over the head.
        std::fs::write(dir.join("s.dat"), b"local-newer").unwrap();
        let remote_items = vec![(
            saves::FileEntry {
                key: "s.dat".into(),
                size: 6,
                sha256: hex::encode(sha2::Sha256::digest(b"remote")),
                mode: 0o644,
            },
            b"remote".to_vec(),
        )];
        let remote_hash = saves::content_hash(&[remote_items[0].0.clone()]);
        *fake.head.lock().unwrap() = Some((3, remote_hash.clone()));
        *fake.archive.lock().unwrap() = saves::build_archive(&remote_items).unwrap();
        fake.versions.lock().unwrap().insert(0, mk_version(3, &remote_hash));

        let summary = sync_game(&fake, &store, "g", 0, &roots).await.unwrap();
        assert!(matches!(summary, SyncSummary::Conflict { server_seq: 3, .. }), "{summary:?}");
        let taken = take_local(&fake, &store, "g", 0, &roots).await.unwrap();
        assert!(matches!(taken, SyncSummary::Pushed { seq: 4, .. }), "{taken:?}");
        // and the cursor tracks the version this machine just created
        let (base, _) = store.save_cursor("g", 0).unwrap();
        assert_eq!(base, 4);

        let _ = std::fs::remove_dir_all(&dir);
    }

    #[tokio::test]
    async fn resolve_roots_filters_platform_and_registry() {
        let tmp = std::env::temp_dir();
        let manifest = crate::api::SaveManifest {
            game_id: "g".into(),
            paths: vec![
                crate::api::SavePath {
                    id: "1".into(),
                    platform: "windows".into(),
                    kind: "file".into(),
                    template: "%APPDATA%\\Game\\saves".into(),
                    recursive: true,
                },
                crate::api::SavePath {
                    id: "2".into(),
                    platform: "*".into(),
                    kind: "registry".into(),
                    template: "HKCU\\Software\\Game".into(),
                    recursive: false,
                },
                crate::api::SavePath {
                    id: "3".into(),
                    platform: std::env::consts::OS.into(),
                    kind: "file".into(),
                    template: format!("{}/saves/**", tmp.display()),
                    recursive: true,
                },
            ],
        };
        let vars = HashMap::new();
        let roots = roots_for(&manifest, std::env::consts::OS, &vars).unwrap();
        assert_eq!(roots.len(), 1, "only the current-platform file root survives");
        assert_eq!(roots[0].base, tmp.join("saves"));
        assert!(roots[0].recursive);
    }

    #[tokio::test]
    async fn resolve_roots_reports_unresolved_placeholder() {
        let manifest = crate::api::SaveManifest {
            game_id: "g".into(),
            paths: vec![crate::api::SavePath {
                id: "1".into(),
                platform: std::env::consts::OS.into(),
                kind: "file".into(),
                template: "{steamid}/save".into(),
                recursive: true,
            }],
        };
        let err = roots_for(&manifest, std::env::consts::OS, &HashMap::new()).unwrap_err();
        assert!(matches!(err, SyncError::Config(ref m) if m.contains("steamid")), "{err}");
    }

    #[tokio::test]
    async fn resolve_roots_errors_when_nothing_matches() {
        let manifest = crate::api::SaveManifest {
            game_id: "g".into(),
            paths: vec![crate::api::SavePath {
                id: "1".into(),
                platform: "windows".into(),
                kind: "file".into(),
                template: "C:\\saves".into(),
                recursive: true,
            }],
        };
        let err = roots_for(&manifest, "linux", &HashMap::new()).unwrap_err();
        assert!(matches!(err, SyncError::Config(_)), "{err}");
    }
}
