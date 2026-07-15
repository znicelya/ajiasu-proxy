use std::{env, path::PathBuf};

use thiserror::Error;

#[derive(Debug, Clone)]
pub struct Config {
    pub node_name: String,
    pub state_directory: PathBuf,
    pub control_plane_endpoint: String,
    pub enrollment_token: Option<String>,
    pub agent_version: String,
    pub architecture: String,
    pub runtime: String,
    pub runner_image: Option<String>,
}

#[derive(Debug, Error)]
pub enum ConfigError {
    #[error("AJIASU_AGENT_NODE_NAME is required")]
    MissingNodeName,
    #[error("AJIASU_AGENT_STATE_DIRECTORY must be absolute")]
    InvalidStateDirectory,
    #[error("AJIASU_AGENT_CONTROL_PLANE_ENDPOINT is required")]
    MissingControlPlaneEndpoint,
    #[error("AJIASU_AGENT_RUNTIME must be docker or process")]
    InvalidRuntime,
    #[error("AJIASU_AGENT_RUNNER_IMAGE must be an immutable digest for docker runtime")]
    InvalidRunnerImage,
}

impl Config {
    pub fn from_env() -> Result<Self, ConfigError> {
        let node_name = env::var("AJIASU_AGENT_NODE_NAME")
            .ok()
            .map(|value| value.trim().to_owned())
            .filter(|value| !value.is_empty())
            .ok_or(ConfigError::MissingNodeName)?;
        let state_directory = env::var_os("AJIASU_AGENT_STATE_DIRECTORY")
            .map(PathBuf::from)
            .unwrap_or_else(|| PathBuf::from("/var/lib/ajiasu-agent"));
        if !state_directory.is_absolute() {
            return Err(ConfigError::InvalidStateDirectory);
        }
        let control_plane_endpoint = env::var("AJIASU_AGENT_CONTROL_PLANE_ENDPOINT")
            .ok()
            .map(|v| v.trim().to_owned())
            .filter(|v| !v.is_empty())
            .ok_or(ConfigError::MissingControlPlaneEndpoint)?;
        let runtime = env::var("AJIASU_AGENT_RUNTIME").unwrap_or_else(|_| "docker".to_owned());
        if runtime != "docker" && runtime != "process" {
            return Err(ConfigError::InvalidRuntime);
        }
        let runner_image = env::var("AJIASU_AGENT_RUNNER_IMAGE")
            .ok()
            .filter(|value| !value.trim().is_empty());
        if runtime == "docker"
            && !runner_image
                .as_deref()
                .is_some_and(|value| value.contains("@sha256:"))
        {
            return Err(ConfigError::InvalidRunnerImage);
        }
        Ok(Self {
            node_name,
            state_directory,
            control_plane_endpoint,
            enrollment_token: env::var("AJIASU_AGENT_ENROLLMENT_TOKEN")
                .ok()
                .filter(|v| !v.trim().is_empty()),
            agent_version: env::var("AJIASU_AGENT_VERSION")
                .unwrap_or_else(|_| env!("CARGO_PKG_VERSION").to_owned()),
            architecture: std::env::consts::ARCH.to_owned(),
            runtime,
            runner_image,
        })
    }
}
