use std::path::Path;
use std::sync::Arc;

use async_trait::async_trait;

use crate::api::ApiClient;
use crate::depot::manifest::Manifest;
use crate::depot::referenced_objects;
use crate::download::{
    assemble_from_cache, ensure_objects, Cache, DownloadError, ObjectSource, Sleeper,
};
use crate::install::InstallReport;
use crate::store::Store;

/// Adapts the network `ApiClient` to the download engine's `ObjectSource`.
struct ApiObjectSource(Arc<dyn ApiClient>);

#[async_trait]
impl ObjectSource for ApiObjectSource {
    async fn fetch(&self, sha256: &str) -> Result<Vec<u8>, String> {
        self.0.get_object(sha256).await.map_err(|e| e.to_string())
    }
}

/// Drives a full install: resolve the release, load and verify its manifest, download
/// and cache the objects, then assemble the files to disk and record completion.
///
/// The client, cache location, store and time source are injected, so the whole flow is
/// exercised in tests with a fake client backed by the Go golden fixture.
pub struct Installer {
    pub client: Arc<dyn ApiClient>,
    pub store: Arc<Store>,
    pub cache_dir: std::path::PathBuf,
    pub concurrency: usize,
    pub max_attempts: u32,
    pub sleeper: Arc<dyn Sleeper>,
}

impl Installer {
    pub async fn install_version(
        &self,
        game_id: &str,
        version_id: &str,
        dest: &Path,
    ) -> Result<InstallReport, DownloadError> {
        let release = self
            .client
            .release(version_id)
            .await
            .map_err(|e| DownloadError::Decode(e.to_string()))?;

        let manifest_bytes = self
            .client
            .get_manifest(&release.digest)
            .await
            .map_err(|e| DownloadError::Decode(e.to_string()))?;
        let manifest = Manifest::from_json(&manifest_bytes)
            .map_err(|e| DownloadError::Decode(format!("manifest: {e}")))?;
        if manifest.digest != release.digest {
            return Err(DownloadError::Decode(format!(
                "manifest digest {} != release digest {}",
                manifest.digest, release.digest
            )));
        }

        let objects = referenced_objects(&manifest, &manifest.files);
        let files: Vec<(String, String)> = manifest
            .files
            .iter()
            .map(|f| (f.path.clone(), f.sha256.clone()))
            .collect();

        self.store.prepare_version(
            version_id,
            game_id,
            &release.digest,
            &dest.display().to_string(),
            &objects,
            &files,
        )?;

        // Skip objects already verified for this version (resume after an interrupted run).
        let have = self.store.done_set(version_id)?;
        let pending: Vec<String> = objects
            .iter()
            .filter(|s| !have.contains(*s))
            .cloned()
            .collect();

        let cache = Arc::new(Cache::new(self.cache_dir.clone()));
        let remote = Arc::new(ApiObjectSource(self.client.clone()));
        ensure_objects(
            &manifest,
            &pending,
            remote,
            cache.clone(),
            &self.store,
            version_id,
            self.concurrency,
            self.sleeper.clone(),
            self.max_attempts,
        )
        .await?;

        assemble_from_cache(&manifest, dest, &cache, &self.store, version_id)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::api::{
        ApiError, LibraryItem, Me, PairingClaim, PairingInitiate, ReleaseInfo,
        SaveManifest, SaveSlot, SaveVersion, PushOutcome,
    };
    use crate::download::SystemSleeper;
    use std::path::PathBuf;

    struct FixtureClient {
        root: PathBuf,
        digest: String,
    }

    #[async_trait]
    impl ApiClient for FixtureClient {
        async fn initiate_pairing(&self, _n: &str, _p: &str) -> Result<PairingInitiate, ApiError> {
            unreachable!()
        }
        async fn claim_pairing(&self, _c: &str) -> Result<PairingClaim, ApiError> {
            unreachable!()
        }
        async fn me(&self) -> Result<Me, ApiError> {
            unreachable!()
        }
        async fn library(&self) -> Result<Vec<LibraryItem>, ApiError> {
            unreachable!()
        }
        async fn release(&self, _v: &str) -> Result<ReleaseInfo, ApiError> {
            Ok(ReleaseInfo {
                digest: self.digest.clone(),
                total_size: 30,
                manifest_url: format!("/api/v1/depot/manifests/{}", self.digest),
            })
        }
        async fn get_manifest(&self, _d: &str) -> Result<Vec<u8>, ApiError> {
            let p = self.root.join("manifests").join(format!("{}.json", self.digest));
            std::fs::read(&p).map_err(|e| ApiError::Transport(e.to_string()))
        }
        async fn get_object(&self, sha: &str) -> Result<Vec<u8>, ApiError> {
            let p = crate::depot::crypto::object_path(&self.root, sha);
            std::fs::read(&p).map_err(|e| ApiError::Transport(e.to_string()))
        }
        async fn post_playtime(&self, _g: &str, _s: u64) -> Result<(), ApiError> {
            Ok(())
        }
        async fn list_saves(&self, _g: Option<&str>) -> Result<Vec<SaveSlot>, ApiError> { unreachable!() }
        async fn save_manifest(&self, _g: &str) -> Result<SaveManifest, ApiError> { unreachable!() }
        async fn push_save(&self, _g: &str, _s: i32, _h: &str, _b: i32, _f: u32, _a: &[u8]) -> Result<PushOutcome, ApiError> { unreachable!() }
        async fn download_save(&self, _g: &str, _s: i32, _seq: Option<i32>) -> Result<Vec<u8>, ApiError> { unreachable!() }
        async fn list_save_versions(&self, _g: &str, _s: i32) -> Result<Vec<SaveVersion>, ApiError> { unreachable!() }
        async fn restore_save(&self, _g: &str, _s: i32, _v: &str) -> Result<SaveVersion, ApiError> { unreachable!() }
        async fn pin_save(&self, _g: &str, _s: i32, _v: &str, _p: bool) -> Result<(), ApiError> { unreachable!() }
        async fn delete_save_version(&self, _g: &str, _s: i32, _v: &str) -> Result<(), ApiError> { unreachable!() }
    }

    #[tokio::test]
    async fn install_version_end_to_end_with_fake_client() {
        let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("fixtures").join("depot");
        let digest = std::fs::read_to_string(root.join("DIGEST")).unwrap();
        let digest = digest.trim().to_string();

        let work = std::env::temp_dir().join(format!("gd-installer-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&work);
        let cache_dir = work.join("cache");
        let dest = work.join("game");

        let installer = Installer {
            client: Arc::new(FixtureClient { root: root.clone(), digest: digest.clone() }),
            store: Arc::new(Store::in_memory().unwrap()),
            cache_dir: cache_dir.clone(),
            concurrency: 4,
            max_attempts: 3,
            sleeper: Arc::new(SystemSleeper),
        };

        let report = installer.install_version("game-1", "ver-1", &dest).await.unwrap();
        assert_eq!(report.files.len(), 2);
        assert_eq!(std::fs::read(dest.join("a.txt")).unwrap(), b"hello world");
        assert_eq!(
            std::fs::read(dest.join("sub/b.txt")).unwrap(),
            b"second file content"
        );
        assert!(installer.store.is_complete("ver-1").unwrap());

        // Second install resumes from cache without re-fetching (objects already done).
        let report2 = installer.install_version("game-1", "ver-1", &dest).await.unwrap();
        assert!(report2.files.is_empty(), "second install should skip both files");
        assert_eq!(report2.skipped.len(), 2);

        std::fs::remove_dir_all(&work).ok();
    }
}
