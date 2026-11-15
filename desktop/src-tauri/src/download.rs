use std::path::PathBuf;
use std::sync::Arc;

use async_trait::async_trait;
use tokio::sync::Semaphore;
use tokio::task::JoinSet;

use crate::depot::crypto::{self, DepotError};
use crate::depot::manifest::Manifest;
use crate::install::{self, InstallError, InstallReport};
use crate::store::{Store, StoreError};

#[derive(Debug, thiserror::Error)]
pub enum DownloadError {
    #[error("io: {0}")]
    Io(#[from] std::io::Error),
    #[error("depot: {0}")]
    Depot(#[from] DepotError),
    #[error("store: {0}")]
    Store(#[from] StoreError),
    #[error("install: {0}")]
    Install(#[from] InstallError),
    #[error("decode: {0}")]
    Decode(String),
    #[error("object {sha} failed after {attempts} attempts: {reason}")]
    Exhausted {
        sha: String,
        attempts: u32,
        reason: String,
    },
}

/// Supplies the raw ciphertext for an object hash. Backed by the network in production
/// and by a fake in tests.
#[async_trait]
pub trait ObjectSource: Send + Sync {
    async fn fetch(&self, sha256: &str) -> Result<Vec<u8>, String>;
}

/// Time source for backoff. Tests use a recorder so they do not actually sleep.
#[async_trait]
pub trait Sleeper: Send + Sync {
    async fn sleep_ms(&self, ms: u64);
}

pub struct SystemSleeper;

#[async_trait]
impl Sleeper for SystemSleeper {
    async fn sleep_ms(&self, ms: u64) {
        tokio::time::sleep(std::time::Duration::from_millis(ms)).await;
    }
}

/// Content-addressed on-disk cache of downloaded objects under `<dir>/<xx>/<sha>`.
pub struct Cache {
    pub dir: PathBuf,
}

impl Cache {
    pub fn new(dir: PathBuf) -> Self {
        Self { dir }
    }
    fn path(&self, sha: &str) -> PathBuf {
        crate::depot::crypto::object_path(&self.dir, sha)
    }
    fn get(&self, sha: &str) -> std::io::Result<Vec<u8>> {
        std::fs::read(self.path(sha))
    }
    fn put(&self, sha: &str, bytes: &[u8]) -> std::io::Result<()> {
        let p = self.path(sha);
        if let Some(parent) = p.parent() {
            std::fs::create_dir_all(parent)?;
        }
        let tmp = p.with_extension("tmp");
        std::fs::write(&tmp, bytes)?;
        std::fs::rename(&tmp, &p)?;
        Ok(())
    }
}

/// Backoff: 200 ms, 400 ms, 800 ms, capped at 5 s.
pub fn backoff_ms(attempt: u32) -> u64 {
    (200u64 << attempt.min(5)).min(5000)
}

/// Fetch, verify by decryption, and cache every pending object, then record completion.
///
/// - Resume: an object already in the cache that decrypts and hashes correctly is not
///   re-fetched.
/// - Retry: a fetch that fails or yields bytes that fail decryption is retried with
///   exponential backoff up to `max_attempts`, then reported.
/// - Integrity: an object is marked done only after AES-256-GCM decrypt plus its own
///   content-hash check succeed, so a corrupt download never silently completes.
pub async fn ensure_objects(
    manifest: &Manifest,
    pending: &[String],
    remote: Arc<dyn ObjectSource>,
    cache: Arc<Cache>,
    store: &Store,
    version_id: &str,
    concurrency: usize,
    sleeper: Arc<dyn Sleeper>,
    max_attempts: u32,
) -> Result<(), DownloadError> {
    let key = crypto::key_bytes(manifest)?;
    let sem = Arc::new(Semaphore::new(concurrency.max(1)));
    let mut set = JoinSet::new();

    for sha in pending.iter() {
        let meta = manifest
            .objects
            .get(sha)
            .ok_or_else(|| DownloadError::Depot(DepotError::MissingObject(sha.clone())))?
            .clone();
        let sha = sha.clone();
        let remote = remote.clone();
        let cache = cache.clone();
        let sem = sem.clone();
        let sleeper = sleeper.clone();

        set.spawn(async move {
            let _permit = sem.acquire().await.expect("semaphore closed");

            // Resume path: already cached and verifiable.
            if let Ok(ct) = cache.get(&sha) {
                if crypto::decrypt_object(&key, &sha, &meta, &ct).is_ok() {
                    return Ok::<String, DownloadError>(sha);
                }
            }

            let mut last_reason = String::from("no attempt");
            for attempt in 1..=max_attempts.max(1) {
                match remote.fetch(&sha).await {
                    Ok(ct) => match crypto::decrypt_object(&key, &sha, &meta, &ct) {
                        Ok(_) => {
                            cache.put(&sha, &ct).map_err(DownloadError::Io)?;
                            return Ok(sha);
                        }
                        Err(e) => last_reason = e.to_string(),
                    },
                    Err(e) => last_reason = e,
                }
                if attempt < max_attempts.max(1) {
                    sleeper.sleep_ms(backoff_ms(attempt - 1)).await;
                }
            }
            Err(DownloadError::Exhausted {
                sha,
                attempts: max_attempts.max(1),
                reason: last_reason,
            })
        });
    }

    while let Some(res) = set.join_next().await {
        match res {
            Ok(Ok(sha)) => store.mark_object_done(version_id, &sha)?,
            Ok(Err(e)) => {
                set.abort_all();
                return Err(e);
            }
            Err(join_err) => {
                set.abort_all();
                return Err(DownloadError::Io(std::io::Error::other(join_err.to_string())));
            }
        }
    }
    Ok(())
}

/// Assemble every file from the cache into `dest`, then record completion.
pub fn assemble_from_cache(
    manifest: &Manifest,
    dest: &std::path::Path,
    cache: &Cache,
    store: &Store,
    version_id: &str,
) -> Result<InstallReport, DownloadError> {
    let mut reader = |sha: &str| cache.get(sha).map_err(|e| e.to_string());
    let report = install::install_release(manifest, dest, &manifest.files, &mut reader)?;
    for f in &manifest.files {
        store.mark_file_done(version_id, &f.path)?;
    }
    store.mark_complete(version_id)?;
    Ok(report)
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::collections::HashMap;
    use std::path::Path;
    use std::sync::Mutex;

    fn fixture() -> crate::depot::FileDepot {
        let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("fixtures").join("depot");
        let digest = std::fs::read_to_string(root.join("DIGEST")).unwrap();
        crate::depot::FileDepot::open(&root, digest.trim()).unwrap()
    }

    struct FakeSource {
        data: HashMap<String, Vec<u8>>,
        fail_first: Mutex<HashMap<String, u32>>,
        calls: Mutex<HashMap<String, u32>>,
    }

    #[async_trait]
    impl ObjectSource for FakeSource {
        async fn fetch(&self, sha: &str) -> Result<Vec<u8>, String> {
            *self.calls.lock().unwrap().entry(sha.to_string()).or_default() += 1;
            let mut fail = self.fail_first.lock().unwrap();
            if let Some(n) = fail.get_mut(sha) {
                if *n > 0 {
                    *n -= 1;
                    return Err("simulated network error".into());
                }
            }
            self.data
                .get(sha)
                .cloned()
                .ok_or_else(|| "not found".to_string())
        }
    }

    struct RecordingSleeper(Mutex<Vec<u64>>);
    #[async_trait]
    impl Sleeper for RecordingSleeper {
        async fn sleep_ms(&self, ms: u64) {
            self.0.lock().unwrap().push(ms);
        }
    }

    fn tempdir(tag: &str) -> PathBuf {
        let d = std::env::temp_dir().join(format!("gd-dl-{tag}-{}", std::process::id()));
        let _ = std::fs::remove_dir_all(&d);
        std::fs::create_dir_all(&d).unwrap();
        d
    }

    #[tokio::test]
    async fn fetches_verifies_and_caches_with_retry() {
        let depot = fixture();
        let objs: Vec<String> =
            crate::depot::referenced_objects(&depot.manifest, &depot.manifest.files);
        let mut data = HashMap::new();
        for o in &objs {
            data.insert(o.clone(), depot.read_object(o).unwrap());
        }
        // First object fails once, then succeeds: exercises retry + backoff.
        let mut fail = HashMap::new();
        fail.insert(objs[0].clone(), 1u32);

        let cache = Arc::new(Cache::new(tempdir("retry")));
        let store = Store::in_memory().unwrap();
        store
            .prepare_version("v1", "g1", "d1", "/dest", &objs, &[])
            .unwrap();

        let sleeper = Arc::new(RecordingSleeper(Mutex::new(vec![])));
        let src = Arc::new(FakeSource {
            data: data.clone(),
            fail_first: Mutex::new(fail),
            calls: Mutex::new(HashMap::new()),
        });

        ensure_objects(
            &depot.manifest,
            &objs,
            src.clone(),
            cache.clone(),
            &store,
            "v1",
            4,
            sleeper.clone(),
            3,
        )
        .await
        .expect("ensure_objects");

        assert!(store.pending_objects("v1").unwrap().is_empty());
        // one retry sleep recorded
        assert_eq!(*sleeper.0.lock().unwrap(), vec![backoff_ms(0)]);
        // the failing object was fetched twice
        assert_eq!(src.calls.lock().unwrap()[&objs[0]], 2);

        // Assemble from cache into a real directory.
        let dest = tempdir("retry-dest");
        let report = assemble_from_cache(&depot.manifest, &dest, &cache, &store, "v1").unwrap();
        assert_eq!(report.files.len(), 2);
        assert_eq!(std::fs::read(dest.join("a.txt")).unwrap(), b"hello world");
        assert!(store.is_complete("v1").unwrap());

        std::fs::remove_dir_all(&cache.dir).ok();
        std::fs::remove_dir_all(&dest).ok();
    }

    #[tokio::test]
    async fn resume_skips_cached_objects() {
        let depot = fixture();
        let objs = crate::depot::referenced_objects(&depot.manifest, &depot.manifest.files);
        let cache = Arc::new(Cache::new(tempdir("resume")));
        // Pre-fill the cache with correct bytes for both objects.
        for o in &objs {
            cache.put(o, &depot.read_object(o).unwrap()).unwrap();
        }
        // A source that errors if asked: proves resume never hits the network.
        struct Panicky;
        #[async_trait]
        impl ObjectSource for Panicky {
            async fn fetch(&self, _sha: &str) -> Result<Vec<u8>, String> {
                Err("network should not be used".into())
            }
        }
        let store = Store::in_memory().unwrap();
        store
            .prepare_version("v1", "g1", "d1", "/dest", &objs, &[])
            .unwrap();

        ensure_objects(
            &depot.manifest,
            &objs,
            Arc::new(Panicky),
            cache.clone(),
            &store,
            "v1",
            2,
            Arc::new(SystemSleeper),
            3,
        )
        .await
        .expect("resume");
        assert!(store.pending_objects("v1").unwrap().is_empty());
        std::fs::remove_dir_all(&cache.dir).ok();
    }

    #[tokio::test]
    async fn corrupt_object_exhausts_and_errors() {
        let depot = fixture();
        // One object only, so the retry/backoff count is deterministic.
        let objs = crate::depot::referenced_objects(&depot.manifest, &depot.manifest.files);
        let one = vec![objs[0].clone()];
        // Source returns wrong bytes: GCM/AAD check must fail every time.
        struct Garbage;
        #[async_trait]
        impl ObjectSource for Garbage {
            async fn fetch(&self, _sha: &str) -> Result<Vec<u8>, String> {
                Ok(vec![0u8; 32])
            }
        }
        let cache = Arc::new(Cache::new(tempdir("corrupt")));
        let store = Store::in_memory().unwrap();
        store
            .prepare_version("v1", "g1", "d1", "/dest", &one, &[])
            .unwrap();
        let sleeper = Arc::new(RecordingSleeper(Mutex::new(vec![])));

        let err = ensure_objects(
            &depot.manifest,
            &one,
            Arc::new(Garbage),
            cache.clone(),
            &store,
            "v1",
            1,
            sleeper.clone(),
            2,
        )
        .await
        .expect_err("must fail");
        assert!(matches!(err, DownloadError::Exhausted { .. }), "{err:?}");
        // still pending, and one sleep between the two attempts
        assert_eq!(store.pending_objects("v1").unwrap().len(), 1);
        assert_eq!(*sleeper.0.lock().unwrap(), vec![backoff_ms(0)]);
        std::fs::remove_dir_all(&cache.dir).ok();
    }
}
