use std::{
    collections::HashMap,
    sync::{Arc, Mutex},
    time::{Duration, Instant},
};

use argon2::{Argon2, PasswordHash, PasswordVerifier};
use thiserror::Error;
use tokio::sync::Semaphore;

#[derive(Debug, Error, Eq, PartialEq)]
pub enum AuthError {
    #[error("authentication failed")]
    Failed,
    #[error("authentication work is busy")]
    Busy,
}

#[derive(Clone)]
pub struct Authenticator {
    semaphore: Arc<Semaphore>,
    failures: Arc<Mutex<HashMap<String, (u32, Instant)>>>,
    max_failures: u32,
}
impl Authenticator {
    pub fn new(max_concurrent: usize, max_failures: u32) -> Self {
        Self {
            semaphore: Arc::new(Semaphore::new(max_concurrent.max(1))),
            failures: Arc::new(Mutex::new(HashMap::new())),
            max_failures: max_failures.max(1),
        }
    }
    pub async fn verify(
        &self,
        source: &str,
        username: &str,
        password: &str,
        verifier: Option<&str>,
    ) -> Result<(), AuthError> {
        if username.is_empty() || username.len() > 128 || password.len() > 1024 {
            return Err(AuthError::Failed);
        }
        let permit = self.semaphore.try_acquire().map_err(|_| AuthError::Busy)?;
        let blocked = {
            let mut failures = self.failures.lock().map_err(|_| AuthError::Failed)?;
            let entry = failures
                .entry(source.to_string())
                .or_insert((0, Instant::now()));
            if entry.1.elapsed() > Duration::from_secs(60) {
                *entry = (0, Instant::now());
            }
            entry.0 >= self.max_failures
        };
        if blocked {
            drop(permit);
            return Err(AuthError::Failed);
        }
        let result = verifier
            .and_then(|value| PasswordHash::new(value).ok())
            .map(|hash| {
                Argon2::default()
                    .verify_password(password.as_bytes(), &hash)
                    .is_ok()
            })
            .unwrap_or(false);
        if !result {
            let mut failures = self.failures.lock().map_err(|_| AuthError::Failed)?;
            let entry = failures
                .entry(source.to_string())
                .or_insert((0, Instant::now()));
            entry.0 = entry.0.saturating_add(1);
        }
        drop(permit);
        if result {
            Ok(())
        } else {
            Err(AuthError::Failed)
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use argon2::{PasswordHasher, password_hash::SaltString};
    #[tokio::test]
    async fn unknown_and_wrong_credentials_share_failure_path() {
        let auth = Authenticator::new(1, 2);
        assert_eq!(
            auth.verify("127.0.0.1", "u", "p", None).await,
            Err(AuthError::Failed)
        );
        assert_eq!(
            auth.verify("127.0.0.1", "u", "p", Some("invalid")).await,
            Err(AuthError::Failed)
        );
        assert_eq!(
            auth.verify("127.0.0.1", "u", "p", None).await,
            Err(AuthError::Failed)
        );
    }

    #[tokio::test]
    async fn verifies_argon2id_and_bounds_work() {
        let salt = SaltString::encode_b64(b"0123456789abcdef").unwrap();
        let verifier = Argon2::default()
            .hash_password(b"secret", &salt)
            .unwrap()
            .to_string();
        let auth = Authenticator::new(1, 3);
        assert_eq!(
            auth.verify("198.51.100.1", "user", "secret", Some(&verifier))
                .await,
            Ok(())
        );
        assert_eq!(
            auth.verify("198.51.100.1", "user", "wrong", Some(&verifier))
                .await,
            Err(AuthError::Failed)
        );
    }
}
