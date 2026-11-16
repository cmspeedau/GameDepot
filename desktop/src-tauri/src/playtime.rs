use crate::api::ApiClient;
use std::time::Instant;

/// Playtime accounting for a single game session. Time is injected through a `Clock`
/// so the accumulator is unit-testable without sleeping or depending on wall time.
pub trait Clock: Send + Sync {
    fn now_ms(&self) -> i64;
}

/// A monotonic wall clock. Used in production.
pub struct RealClock;
impl Clock for RealClock {
    fn now_ms(&self) -> i64 {
        static ORIGIN: std::sync::OnceLock<Instant> = std::sync::OnceLock::new();
        let origin = ORIGIN.get_or_init(Instant::now);
        origin.elapsed().as_millis() as i64
    }
}

pub struct PlaySession<C: Clock> {
    clock: C,
    started_ms: Option<i64>,
    accumulated_ms: i64,
}

impl<C: Clock> PlaySession<C> {
    pub fn new(clock: C) -> Self {
        Self { clock, started_ms: None, accumulated_ms: 0 }
    }

    pub fn is_running(&self) -> bool {
        self.started_ms.is_some()
    }

    pub fn start(&mut self) {
        if self.started_ms.is_none() {
            self.started_ms = Some(self.clock.now_ms());
        }
    }

    pub fn stop(&mut self) {
        if let Some(s) = self.started_ms.take() {
            let now = self.clock.now_ms();
            if now > s {
                self.accumulated_ms += now - s;
            }
        }
    }

    pub fn elapsed_ms(&self) -> i64 {
        let extra = self
            .started_ms
            .map(|s| {
                let now = self.clock.now_ms();
                if now > s { now - s } else { 0 }
            })
            .unwrap_or(0);
        self.accumulated_ms + extra
    }

    pub fn elapsed_secs(&self) -> u64 {
        (self.elapsed_ms() / 1000) as u64
    }

    pub fn reset(&mut self) {
        self.started_ms = None;
        self.accumulated_ms = 0;
    }

    /// Clamp a reported delta to the server's accepted range (1..31,536,000).
    pub fn clamp_delta(secs: u64) -> u64 {
        secs.clamp(1, 31_536_000)
    }
}

/// Report a finished session's elapsed seconds to the server. Returns Ok without
/// calling when the elapsed delta is non-positive (clock skew or a sub-second
/// session). The delta is clamped to the server's accepted range.
pub async fn report_session(
    client: &dyn ApiClient,
    game_id: &str,
    started_ms: i64,
    stopped_ms: i64,
) -> Result<(), crate::api::ApiError> {
    let delta_ms = stopped_ms - started_ms;
    if delta_ms <= 0 {
        return Ok(());
    }
    let secs = (delta_ms / 1000) as u64;
    if secs == 0 {
        return Ok(());
    }
    client
        .post_playtime(game_id, PlaySession::<RealClock>::clamp_delta(secs))
        .await
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::api::{ApiError, LibraryItem, Me, PairingClaim, PairingInitiate, ReleaseInfo};
    use async_trait::async_trait;
    use std::sync::atomic::{AtomicI64, Ordering};
    use std::sync::Arc;
    use std::sync::Mutex;

    #[derive(Clone)]
    struct FakeClock(Arc<AtomicI64>);
    impl FakeClock {
        fn new(start: i64) -> Self { Self(Arc::new(AtomicI64::new(start))) }
        fn set(&self, v: i64) { self.0.store(v, Ordering::SeqCst); }
    }
    impl Clock for FakeClock {
        fn now_ms(&self) -> i64 { self.0.load(Ordering::SeqCst) }
    }

    #[test]
    fn accumulates_across_start_stop() {
        let c = FakeClock::new(1000);
        let mut s = PlaySession::new(c.clone());
        s.start();
        c.set(2500);
        s.stop();
        assert_eq!(s.elapsed_ms(), 1500);
        assert!(!s.is_running());

        s.start();
        c.set(4000);
        s.stop();
        assert_eq!(s.elapsed_ms(), 3000);
    }

    #[test]
    fn stop_without_start_is_noop_and_start_is_idempotent() {
        let c = FakeClock::new(0);
        let mut s = PlaySession::new(c.clone());
        s.stop();
        assert_eq!(s.elapsed_ms(), 0);
        s.start();
        c.set(100);
        s.start();
        s.stop();
        assert_eq!(s.elapsed_ms(), 100);
    }

    #[test]
    fn elapsed_while_running() {
        let c = FakeClock::new(0);
        let mut s = PlaySession::new(c.clone());
        s.start();
        c.set(750);
        assert_eq!(s.elapsed_ms(), 750);
        assert!(s.is_running());
        s.reset();
        assert_eq!(s.elapsed_ms(), 0);
        assert!(!s.is_running());
    }

    #[test]
    fn clamp_bounds() {
        assert_eq!(PlaySession::<RealClock>::clamp_delta(0), 1);
        assert_eq!(PlaySession::<RealClock>::clamp_delta(500), 500);
        assert_eq!(PlaySession::<RealClock>::clamp_delta(100_000_000), 31_536_000);
    }

    struct Mock { calls: Mutex<Vec<(String, u64)>> }
    #[async_trait]
    impl ApiClient for Mock {
        async fn initiate_pairing(&self, _n: &str, _p: &str) -> Result<PairingInitiate, ApiError> { unreachable!() }
        async fn claim_pairing(&self, _c: &str) -> Result<PairingClaim, ApiError> { unreachable!() }
        async fn me(&self) -> Result<Me, ApiError> { unreachable!() }
        async fn library(&self) -> Result<Vec<LibraryItem>, ApiError> { unreachable!() }
        async fn release(&self, _v: &str) -> Result<crate::api::ReleaseInfo, ApiError> { unreachable!() }
        async fn get_manifest(&self, _d: &str) -> Result<Vec<u8>, ApiError> { unreachable!() }
        async fn get_object(&self, _s: &str) -> Result<Vec<u8>, ApiError> { unreachable!() }
        async fn post_playtime(&self, g: &str, s: u64) -> Result<(), ApiError> {
            self.calls.lock().unwrap().push((g.to_string(), s));
            Ok(())
        }
        async fn list_saves(&self, _g: Option<&str>) -> Result<Vec<crate::api::SaveSlot>, ApiError> { unreachable!() }
        async fn save_manifest(&self, _g: &str) -> Result<crate::api::SaveManifest, ApiError> { unreachable!() }
        async fn push_save(&self, _g: &str, _s: i32, _h: &str, _b: i32, _f: u32, _a: &[u8]) -> Result<crate::api::PushOutcome, ApiError> { unreachable!() }
        async fn download_save(&self, _g: &str, _s: i32, _seq: Option<i32>) -> Result<Vec<u8>, ApiError> { unreachable!() }
        async fn list_save_versions(&self, _g: &str, _s: i32) -> Result<Vec<crate::api::SaveVersion>, ApiError> { unreachable!() }
        async fn restore_save(&self, _g: &str, _s: i32, _v: &str) -> Result<crate::api::SaveVersion, ApiError> { unreachable!() }
        async fn pin_save(&self, _g: &str, _s: i32, _v: &str, _p: bool) -> Result<(), ApiError> { unreachable!() }
        async fn delete_save_version(&self, _g: &str, _s: i32, _v: &str) -> Result<(), ApiError> { unreachable!() }
    }

    #[tokio::test]
    async fn report_session_calls_post_playtime_with_clamped_delta() {
        let mock = Mock { calls: Mutex::new(vec![]) };
        report_session(&mock, "game-1", 1000, 2500).await.unwrap();
        assert_eq!(mock.calls.lock().unwrap().as_slice(), &[("game-1".to_string(), 1)]);

        let mock2 = Mock { calls: Mutex::new(vec![]) };
        report_session(&mock2, "game-1", 1000, 1500).await.unwrap();
        assert!(mock2.calls.lock().unwrap().is_empty());

        let mock3 = Mock { calls: Mutex::new(vec![]) };
        report_session(&mock3, "game-1", 2000, 1000).await.unwrap();
        assert!(mock3.calls.lock().unwrap().is_empty());
    }
}
