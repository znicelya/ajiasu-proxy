use std::sync::atomic::{AtomicUsize, Ordering};

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
