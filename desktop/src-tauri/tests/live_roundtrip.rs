//! Live round-trip against a running GameDepot server. Ignored by default.
//!
//! Run with:
//!   GAMEDEPOT_TEST_URL=http://localhost:4000 \
//!   GAMEDEPOT_TEST_TOKEN=<bearer token> \
//!   GAMEDEPOT_TEST_VERSION_ID=<version uuid> \
//!   cargo test --test live_roundtrip -- --ignored --nocapture

use app_lib::api::{HttpApiClient, ApiClient};
use sha2::Digest;
use app_lib::depot::crypto;
use app_lib::depot::manifest::Manifest;
use app_lib::download::SystemSleeper;
use app_lib::install::InstallReport;
use app_lib::installer::Installer;
use app_lib::store::Store;
use std::sync::Arc;

#[tokio::test]
#[ignore]
async fn live_roundtrip() {
    let url = std::env::var("GAMEDEPOT_TEST_URL").expect("GAMEDEPOT_TEST_URL");
    let token = std::env::var("GAMEDEPOT_TEST_TOKEN").expect("GAMEDEPOT_TEST_TOKEN");
    let version_id = std::env::var("GAMEDEPOT_TEST_VERSION_ID").expect("GAMEDEPOT_TEST_VERSION_ID");

    let client = HttpApiClient::new(url.clone(), Some(token.clone())).unwrap();

    let me = client.me().await.expect("me");
    println!("signed in as {}", me.username);

    let library = client.library().await.expect("library");
    assert!(!library.is_empty(), "library empty");
    println!("library: {} games", library.len());

    let release = client.release(&version_id).await.expect("release");
    println!("release digest {} total {}", release.digest, release.total_size);

    let manifest_bytes = client.get_manifest(&release.digest).await.expect("manifest");
    let manifest = Manifest::from_json(&manifest_bytes).expect("parse manifest");
    assert_eq!(manifest.digest, release.digest);
    println!("manifest: {} files, {} objects", manifest.files.len(), manifest.objects.len());

    // Decrypt + verify every object over HTTP.
    let key = crypto::key_bytes(&manifest).unwrap();
    for sha in manifest.objects.keys() {
        let ct = client.get_object(sha).await.expect("get_object");
        let pt = crypto::decrypt_object(&key, sha, manifest.objects.get(sha).unwrap(), &ct)
            .unwrap_or_else(|e| panic!("decrypt {sha}: {e}"));
        let h = sha2::Sha256::digest(&pt);
        assert_eq!(hex::encode(h), *sha, "content hash mismatch for {sha}");
    }
    println!("all {} objects decrypt and verify", manifest.objects.len());

    // Install into a temp dir via the real Installer.
    let store = Store::in_memory().unwrap();
    let cache_dir = std::env::temp_dir().join("gd-live-cache");
    let dest = std::env::temp_dir().join("gd-live-dest");
    let _ = std::fs::remove_dir_all(&cache_dir);
    let _ = std::fs::remove_dir_all(&dest);

    let installer = Installer {
        client: Arc::new(client.clone()),
        store: Arc::new(store),
        cache_dir: cache_dir.clone(),
        concurrency: 4,
        max_attempts: 3,
        sleeper: Arc::new(SystemSleeper),
    };
    let report: InstallReport = installer.install_version("g", &version_id, &dest).await.expect("install");
    println!("installed {} files, {} bytes", report.files.len(), report.bytes);
    assert!(!report.files.is_empty());

    for f in &manifest.files {
        let bytes = std::fs::read(dest.join(&f.path)).expect("file missing");
        let mut h = sha2::Sha256::new();
        use sha2::Digest;
        h.update(&bytes);
        assert_eq!(hex::encode(h.finalize()), f.sha256, "file hash mismatch {}", f.path);
    }
    println!("all files verify on disk");

    let _ = std::fs::remove_dir_all(&dest);
    let _ = std::fs::remove_dir_all(&cache_dir);
}
