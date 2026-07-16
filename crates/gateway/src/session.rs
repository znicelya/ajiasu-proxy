use serde::{Deserialize, Serialize};
use std::path::Path;
use thiserror::Error;

use crate::private_file;

#[derive(Clone, Serialize, Deserialize)]
pub struct SessionState {
    pub gateway_id: String,
    pub gateway_instance_id: String,
    pub session_token: String,
    pub protocol_revision: u32,
}

impl std::fmt::Debug for SessionState {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        formatter
            .debug_struct("SessionState")
            .field("gateway_id", &self.gateway_id)
            .field("gateway_instance_id", &self.gateway_instance_id)
            .field("session_token", &"[redacted]")
            .field("protocol_revision", &self.protocol_revision)
            .finish()
    }
}

#[derive(Debug, Error)]
pub enum SessionError {
    #[error("gateway session state is unavailable")]
    Unavailable,
    #[error("gateway session state is invalid")]
    Invalid,
}

pub fn load(path: &Path) -> Result<Option<SessionState>, SessionError> {
    if !path.exists() {
        return Ok(None);
    }
    let mut bytes =
        private_file::read(path, 2, 64 * 1024).map_err(|_| SessionError::Unavailable)?;
    let state: SessionState = serde_json::from_slice(&bytes).map_err(|_| SessionError::Invalid)?;
    bytes.fill(0);
    validate(&state)?;
    Ok(Some(state))
}

pub fn save(path: &Path, state: &SessionState) -> Result<(), SessionError> {
    validate(state)?;
    let mut bytes = serde_json::to_vec(state).map_err(|_| SessionError::Invalid)?;
    let result = private_file::atomic_write(path, &bytes).map_err(|_| SessionError::Unavailable);
    bytes.fill(0);
    result
}

pub fn retire_enrollment(path: Option<&Path>) -> Result<(), SessionError> {
    let Some(path) = path else {
        return Ok(());
    };
    match std::fs::remove_file(path) {
        Ok(()) => Ok(()),
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => Ok(()),
        // Compose secrets are immutable bind mounts. The server has already
        // consumed the one-time enrollment before session state is persisted,
        // so EACCES/EBUSY/EROFS still leaves the material unusable.
        Err(error)
            if error.kind() == std::io::ErrorKind::PermissionDenied
                || matches!(error.raw_os_error(), Some(16) | Some(30)) =>
        {
            Ok(())
        }
        Err(_) => Err(SessionError::Unavailable),
    }
}

fn validate(state: &SessionState) -> Result<(), SessionError> {
    if state.gateway_id.trim().is_empty()
        || state.gateway_instance_id.trim().is_empty()
        || state.session_token.trim().is_empty()
        || state.protocol_revision != 1
    {
        return Err(SessionError::Invalid);
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use std::fs;

    use super::*;

    #[test]
    fn persists_redacted_session_and_retires_enrollment() {
        let root =
            std::env::temp_dir().join(format!("ajiasu-gateway-session-{}", uuid::Uuid::now_v7()));
        let path = root.join("session.json");
        let enrollment = root.join("enrollment");
        private_file::atomic_write(&enrollment, b"one-time").unwrap();
        let state = SessionState {
            gateway_id: "gateway-a".to_owned(),
            gateway_instance_id: uuid::Uuid::now_v7().to_string(),
            session_token: "gateway-session-canary".to_owned(),
            protocol_revision: 1,
        };
        save(&path, &state).unwrap();
        retire_enrollment(Some(&enrollment)).unwrap();
        let loaded = load(&path).unwrap().unwrap();
        assert!(!format!("{loaded:?}").contains("gateway-session-canary"));
        assert!(!enrollment.exists());
        fs::remove_dir_all(root).unwrap();
    }
}
