/// Playtime accounting for a single game session. Time is injected through a `Clock`
/// so the accumulator is unit-testable without sleeping or depending on wall time.
use std::time::Instant;

pub trait Clock: Send + Sync {
    fn now_ms(&self) -> i64;
}

/// A monotonic wall clock. Used in production.
pub struct RealClock;
impl Clock for RealClock {
    fn now_ms(&self) -> i64 {
        // Instant is monotonic; epoch offset is irrelevant for deltas, but we return
        // millis since an arbitrary origin to stay in the i64 range.
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

#[cfg(test)]
mod tests {
    use super::*;
    use std::sync::atomic::{AtomicI64, Ordering};
    use std::sync::Arc;

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

        // A second session adds to the total.
        s.start();
        c.set(4000);
        s.stop();
        assert_eq!(s.elapsed_ms(), 3000);
    }

    #[test]
    fn stop_without_start_is_noop_and_start_is_idempotent() {
        let c = FakeClock::new(0);
        let mut s = PlaySession::new(c.clone());
        s.stop(); // no-op
        assert_eq!(s.elapsed_ms(), 0);
        s.start();
        c.set(100);
        s.start(); // second start ignored
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
}
