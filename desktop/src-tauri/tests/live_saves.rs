//! Live cloud-save round trip against a running GameDepot server. Ignored by default.
//!
//! Run with:
//!   GAMEDEPOT_TEST_URL=http://localhost:4000 \
//!   GAMEDEPOT_TEST_TOKEN=<bearer token> \
//!   GAMEDEPOT_TEST_GAME=<game uuid> \
//!   cargo test --test live_saves -- --ignored --nocapture
//!
//! Two in-memory cursors stand in for two devices on the same account and slot; the
//! save files live in throwaway directories under the system temp dir.

use app_lib::api::{ApiClient, HttpApiClient};
use app_lib::saves::{self, SaveRoot};
use app_lib::save_sync::{self, SyncSummary};
use app_lib::store::Store;
use std::path::PathBuf;

fn tmpdir(tag: &str) -> PathBuf {
    let dir = std::env::temp_dir().join(format!(
        "gd-live-saves-{}-{}",
        tag,
        std::process::id()
    ));
    let _ = std::fs::remove_dir_all(&dir);
    std::fs::create_dir_all(&dir).unwrap();
    dir
}

fn write_save(dir: &PathBuf, name: &str, bytes: &[u8]) {
    std::fs::write(dir.join(name), bytes).unwrap();
}

#[tokio::test]
#[ignore]
async fn live_save_roundtrip() {
    let url = std::env::var("GAMEDEPOT_TEST_URL").expect("GAMEDEPOT_TEST_URL");
    let token = std::env::var("GAMEDEPOT_TEST_TOKEN").expect("GAMEDEPOT_TEST_TOKEN");
    let game = std::env::var("GAMEDEPOT_TEST_GAME").expect("GAMEDEPOT_TEST_GAME");

    let client = HttpApiClient::new(url, Some(token)).unwrap();
    // A fresh slot per run keeps the test self-contained: reruns against the same
    // server do not collide with earlier head versions.
    let slot: i32 = {
        let micros = std::time::SystemTime::now()
            .duration_since(std::time::UNIX_EPOCH)
            .unwrap()
            .subsec_micros();
        (micros % 1_000_000) as i32 + 1
    };

    // The manifest→roots resolution chain (server template, {p} expansion, /** strip)
    // over real HTTP, independent of the device sync below. The inserted template is
    // `/tmp/opencode/saves-{p}/**` and the game is named SaveLive.
    let roots = save_sync::resolve_roots(&client, &game).await.expect("resolve_roots");
    assert_eq!(roots.len(), 1, "{roots:?}");
    assert!(roots[0].recursive, "{roots:?}");
    assert!(
        roots[0]
            .base
            .ends_with("saves-SaveLive"),
        "{{p}} must expand to the game name: {:?}",
        roots[0].base
    );

    let dir_a = tmpdir("a");
    let dir_b = tmpdir("b");
    let roots_a = vec![SaveRoot {
        label: "root".into(),
        base: dir_a.clone(),
        recursive: true,
    }];
    let roots_b = vec![SaveRoot {
        label: "root".into(),
        base: dir_b.clone(),
        recursive: true,
    }];

    // Device A: first sync pushes the local set.
    write_save(&dir_a, "slot1.dat", b"save v1");
    let store_a = Store::in_memory().unwrap();
    let summary = save_sync::sync_game(&client, &store_a, &game, slot, &roots_a)
        .await
        .expect("push A");
    assert!(matches!(summary, SyncSummary::Pushed { seq: 1, .. }), "{summary:?}");

    // Re-sync with nothing changed: noop, no new version.
    let summary = save_sync::sync_game(&client, &store_a, &game, slot, &roots_a)
        .await
        .unwrap();
    assert!(matches!(summary, SyncSummary::Noop { seq: 1, .. }), "{summary:?}");

    // Device A changes a file; A pushes again.
    write_save(&dir_a, "slot1.dat", b"save v2");
    let summary = save_sync::sync_game(&client, &store_a, &game, slot, &roots_a)
        .await
        .unwrap();
    assert!(matches!(summary, SyncSummary::Pushed { seq: 2, .. }), "{summary:?}");

    // Device B has never synced: pulling must bring v2 bytes down and verify hashes.
    let store_b = Store::in_memory().unwrap();
    let summary = save_sync::sync_game(&client, &store_b, &game, slot, &roots_b)
        .await
        .unwrap();
    match summary {
        SyncSummary::Pulled {
            seq,
            files_written,
            content_hash,
            ..
        } => {
            assert_eq!(seq, 2);
            assert_eq!(files_written, 1);
            let bytes = std::fs::read(dir_b.join("slot1.dat")).unwrap();
            assert_eq!(bytes, b"save v2");
            // the placed file hashes to the announced content hash of the version
            let entry = saves::entry_from_file("slot1.dat", &dir_b.join("slot1.dat")).unwrap();
            assert_eq!(
                saves::content_hash(&[entry]),
                content_hash,
                "pulled bytes must match the version content hash"
            );
        }
        other => panic!("expected pulled, got {other:?}"),
    }

    // Both sides change -> conflict, then "keep mine" from B resolves it by pushing.
    write_save(&dir_a, "extra.dat", b"a-only");
    save_sync::sync_game(&client, &store_a, &game, slot, &roots_a)
        .await
        .unwrap();
    write_save(&dir_b, "extra.dat", b"b-only");
    let summary = save_sync::sync_game(&client, &store_b, &game, slot, &roots_b)
        .await
        .unwrap();
    assert!(matches!(summary, SyncSummary::Conflict { .. }), "{summary:?}");

    let summary = save_sync::take_local(&client, &store_b, &game, slot, &roots_b)
        .await
        .unwrap();
    assert!(matches!(summary, SyncSummary::Pushed { .. }), "{summary:?}");

    // And "use server" from A lands B's newest bytes locally.
    let summary = save_sync::take_server(&client, &store_a, &game, slot, &roots_a)
        .await
        .unwrap();
    assert!(matches!(summary, SyncSummary::Pulled { .. }), "{summary:?}");
    assert_eq!(std::fs::read(dir_a.join("extra.dat")).unwrap(), b"b-only");

    // Version history is visible and newest first.
    let versions = client.list_save_versions(&game, slot).await.unwrap();
    assert!(versions.len() >= 4, "versions = {:?}", versions.len());
    assert!(versions[0].seq > versions[versions.len() - 1].seq);

    // Restore an older version, then pull it from A.
    let old = versions.iter().rev().find(|v| v.kind == "save").unwrap();
    client
        .restore_save(&game, slot, &old.id)
        .await
        .expect("restore");
    let summary = save_sync::take_server(&client, &store_a, &game, slot, &roots_a)
        .await
        .unwrap();
    assert!(matches!(summary, SyncSummary::Pulled { .. }), "{summary:?}");
    let content = std::fs::read_to_string(dir_a.join("slot1.dat")).unwrap();
    assert!(content.starts_with("save v"), "restored {content:?}");

    let _ = std::fs::remove_dir_all(&dir_a);
    let _ = std::fs::remove_dir_all(&dir_b);
    println!("live save round trip ok");
}
