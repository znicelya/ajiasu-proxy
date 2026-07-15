use serde::{Deserialize, Serialize};
use std::{fs, path::Path};
use thiserror::Error;

#[derive(Debug, Clone, Serialize, Deserialize)]
pub struct SessionState {
    pub node_id: String,
    pub session_token: String,
    pub protocol_revision: u32,
}

#[derive(Debug, Error)]
pub enum SessionError {
    #[error("session file could not be read")]
    Read(#[from] std::io::Error),
    #[error("session file is invalid")]
    Decode(#[from] serde_json::Error),
}

pub fn load(path: &Path) -> Result<Option<SessionState>, SessionError> {
    match fs::read(path) {
        Ok(bytes) => Ok(Some(serde_json::from_slice(&bytes)?)),
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => Ok(None),
        Err(error) => Err(error.into()),
    }
}

pub fn save(path: &Path, state: &SessionState) -> Result<(), SessionError> {
    if let Some(parent) = path.parent() {
        fs::create_dir_all(parent)?;
    }
    let bytes = serde_json::to_vec(state)?;
    fs::write(path, bytes)?;
    Ok(())
}
