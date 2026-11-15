//! End-to-end of the local pipeline (no network): open a Go-built release, install it
//! into a fresh directory through the content-addressed objects, and record completion
//! in the install store.

use std::collections::HashSet;
use std::path::{Path, PathBuf};

use app_lib::depot::{missing_objects, referenced_objects, FileDepot};
use app_lib::install;
use app_lib::store::Store;

fn fixture_root() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("fixtures").join("depot")
}

#[test]
fn install_then_record_completion() {
    let root = fixture_root();
    let digest = std::fs::read_to_string(root.join("DIGEST")).unwrap();
    let digest = digest.trim().to_string();
    let depot = FileDepot::open(&root, &digest).unwrap();

    let version_id = "version-abc";
    let game_id = "game-123";
    let dest = std::env::temp_dir().join(format!("gd-flow-{}", std::process::id()));
    let _ = std::fs::remove_dir_all(&dest);

    let objs = referenced_objects(&depot.manifest, &depot.manifest.files);
    let files: Vec<(String, String)> =
        depot.manifest.files.iter().map(|f| (f.path.clone(), f.sha256.clone())).collect();

    let store = Store::in_memory().unwrap();
    store.prepare_version(version_id, game_id, &digest, &dest.display().to_string(), &objs, &files).unwrap();

    // Nothing done yet: everything is pending.
    assert_eq!(missing_objects(&depot.manifest.files, &HashSet::new()).len(), objs.len());

    // Install: decrypt, verify, and write every file.
    let mut reader = |sha: &str| depot.read_object(sha);
    let report =
        install::install_release(&depot.manifest, &dest, &depot.manifest.files, &mut reader)
            .expect("install");
    assert_eq!(report.files.len(), 2);

    for sha in &objs {
        store.mark_object_done(version_id, sha).unwrap();
    }
    for (path, _) in &files {
        store.mark_file_done(version_id, path).unwrap();
    }
    store.mark_complete(version_id).unwrap();

    // State now reports a complete, installed version and nothing pending.
    assert!(store.is_complete(version_id).unwrap());
    assert!(store.pending_objects(version_id).unwrap().is_empty());
    assert_eq!(
        store.installed_version_for_game(game_id).unwrap().as_deref(),
        Some(version_id)
    );

    // Files on disk are exactly right.
    assert_eq!(std::fs::read(dest.join("a.txt")).unwrap(), b"hello world");
    assert_eq!(std::fs::read(dest.join("sub/b.txt")).unwrap(), b"second file content");

    std::fs::remove_dir_all(&dest).ok();
}
