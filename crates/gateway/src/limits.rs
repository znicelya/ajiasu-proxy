use std::{
    sync::{
        Mutex,
        atomic::{AtomicUsize, Ordering},
    },
    time::{Duration, Instant},
};

pub struct ConnectionPermit<'a> {
    counter: &'a AtomicUsize,
}
impl Drop for ConnectionPermit<'_> {
    fn drop(&mut self) {
        self.counter.fetch_sub(1, Ordering::AcqRel);
    }
}
pub fn try_acquire(counter: &AtomicUsize, maximum: usize) -> Option<ConnectionPermit<'_>> {
    let mut current = counter.load(Ordering::Acquire);
    loop {
        if current >= maximum {
            return None;
        }
        match counter.compare_exchange_weak(
            current,
            current + 1,
            Ordering::AcqRel,
            Ordering::Acquire,
        ) {
            Ok(_) => return Some(ConnectionPermit { counter }),
            Err(observed) => current = observed,
        }
    }
}

pub struct RateLimiter {
    state: Mutex<RateState>,
    rate_per_second: f64,
    burst: f64,
}
struct RateState {
    tokens: f64,
    updated: Instant,
}
impl RateLimiter {
    pub fn new(rate_per_second: u32, burst: u32) -> Self {
        let burst = burst.max(1) as f64;
        Self {
            state: Mutex::new(RateState {
                tokens: burst,
                updated: Instant::now(),
            }),
            rate_per_second: rate_per_second.max(1) as f64,
            burst,
        }
    }
    pub fn try_take(&self, amount: u32) -> bool {
        let Ok(mut state) = self.state.lock() else {
            return false;
        };
        let now = Instant::now();
        state.tokens = (state.tokens
            + now.duration_since(state.updated).as_secs_f64() * self.rate_per_second)
            .min(self.burst);
        state.updated = now;
        let amount = amount as f64;
        if state.tokens < amount {
            false
        } else {
            state.tokens -= amount;
            true
        }
    }
}

pub struct ConnectionBudget {
    max_bytes: u64,
    used_bytes: u64,
    idle_timeout: Duration,
    last_activity: Instant,
}
impl ConnectionBudget {
    pub fn new(max_bytes: u64, idle_timeout: Duration) -> Self {
        Self {
            max_bytes,
            used_bytes: 0,
            idle_timeout,
            last_activity: Instant::now(),
        }
    }
    pub fn consume(&mut self, bytes: u64) -> bool {
        if self.max_bytes != 0 && self.used_bytes.saturating_add(bytes) > self.max_bytes {
            return false;
        };
        self.used_bytes = self.used_bytes.saturating_add(bytes);
        self.last_activity = Instant::now();
        true
    }
    pub fn idle_expired(&self) -> bool {
        self.idle_timeout > Duration::ZERO && self.last_activity.elapsed() >= self.idle_timeout
    }
    pub fn used_bytes(&self) -> u64 {
        self.used_bytes
    }
}

pub struct WindowBudget {
    max_bytes: u64,
    used_bytes: u64,
    window: Duration,
    started: Instant,
}
impl WindowBudget {
    pub fn new(max_bytes: u64, window: Duration) -> Self {
        Self {
            max_bytes,
            used_bytes: 0,
            window,
            started: Instant::now(),
        }
    }
    pub fn try_consume(&mut self, bytes: u64) -> bool {
        if self.started.elapsed() >= self.window {
            self.started = Instant::now();
            self.used_bytes = 0;
        }
        if self.max_bytes != 0 && self.used_bytes.saturating_add(bytes) > self.max_bytes {
            return false;
        };
        self.used_bytes = self.used_bytes.saturating_add(bytes);
        true
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    #[test]
    fn permit_releases_once() {
        let counter = AtomicUsize::new(0);
        {
            let permit = try_acquire(&counter, 1).unwrap();
            assert!(try_acquire(&counter, 1).is_none());
            drop(permit);
        }
        assert!(try_acquire(&counter, 1).is_some());
    }
    #[test]
    fn rate_and_bytes_have_hard_boundaries() {
        let limiter = RateLimiter::new(1, 2);
        assert!(limiter.try_take(2));
        assert!(!limiter.try_take(1));
        let mut budget = ConnectionBudget::new(10, Duration::from_secs(1));
        assert!(budget.consume(10));
        assert!(!budget.consume(1));
        let mut window = WindowBudget::new(5, Duration::from_secs(1));
        assert!(window.try_consume(5));
        assert!(!window.try_consume(1));
        assert_eq!(budget.used_bytes(), 10);
    }
}
