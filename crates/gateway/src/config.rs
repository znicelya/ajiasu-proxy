use std::{env, ffi::OsString, net::SocketAddr, path::PathBuf, time::Duration};

use thiserror::Error;

use crate::{private_file, secret::SecretBytes};

pub struct EnrollmentSecret {
    pub value: SecretBytes,
    pub source_path: Option<PathBuf>,
}

impl std::fmt::Debug for EnrollmentSecret {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        formatter.write_str("EnrollmentSecret([redacted])")
    }
}

#[derive(Debug)]
pub struct GatewayConfig {
    pub gateway_name: String,
    pub http_listen: SocketAddr,
    pub socks5_listen: SocketAddr,
    pub control_endpoint: String,
    pub state_directory: PathBuf,
    pub enrollment: Option<EnrollmentSecret>,
    pub max_header_bytes: usize,
    pub max_connections: usize,
    pub shutdown_timeout: Duration,
}

#[derive(Debug, Error, Eq, PartialEq)]
pub enum ConfigError {
    #[error("gateway listener addresses must be distinct")]
    DuplicateListeners,
    #[error("AJIASU_GATEWAY_CONTROL_PLANE_ENDPOINT is required")]
    MissingControlEndpoint,
    #[error("gateway bounds are invalid")]
    InvalidBounds,
    #[error("AJIASU_GATEWAY_NAME is required")]
    MissingGatewayName,
    #[error("gateway listener address is invalid")]
    InvalidListener,
    #[error("AJIASU_GATEWAY_STATE_DIRECTORY must be absolute")]
    InvalidStateDirectory,
    #[error("AJIASU_GATEWAY_ENROLLMENT_TOKEN conflicts with AJIASU_GATEWAY_ENROLLMENT_TOKEN_FILE")]
    EnrollmentConflict,
    #[error("AJIASU_GATEWAY_ENROLLMENT_TOKEN_FILE is invalid")]
    InvalidEnrollmentFile,
    #[error("AJIASU_GATEWAY_SHUTDOWN_TIMEOUT must be between 1s and 300s")]
    InvalidShutdownTimeout,
}

impl GatewayConfig {
    pub fn from_env() -> Result<Self, ConfigError> {
        Self::from_lookup(|name| env::var_os(name))
    }

    pub fn from_lookup<F>(mut lookup: F) -> Result<Self, ConfigError>
    where
        F: FnMut(&str) -> Option<OsString>,
    {
        let gateway_name = text(&mut lookup, "AJIASU_GATEWAY_NAME")
            .filter(|value| !value.is_empty())
            .ok_or(ConfigError::MissingGatewayName)?;
        let http_listen = parse_address(
            text(&mut lookup, "AJIASU_GATEWAY_HTTP_LISTEN")
                .as_deref()
                .unwrap_or("0.0.0.0:8080"),
        )?;
        let socks5_listen = parse_address(
            text(&mut lookup, "AJIASU_GATEWAY_SOCKS5_LISTEN")
                .as_deref()
                .unwrap_or("0.0.0.0:1080"),
        )?;
        let control_endpoint = text(&mut lookup, "AJIASU_GATEWAY_CONTROL_PLANE_ENDPOINT")
            .filter(|value| !value.is_empty())
            .ok_or(ConfigError::MissingControlEndpoint)?;
        let state_directory = lookup("AJIASU_GATEWAY_STATE_DIRECTORY")
            .map(PathBuf::from)
            .unwrap_or_else(|| PathBuf::from("/var/lib/ajiasu-gateway"));
        if !container_absolute(&state_directory) {
            return Err(ConfigError::InvalidStateDirectory);
        }
        let max_header_bytes = parse_usize(
            text(&mut lookup, "AJIASU_GATEWAY_MAX_HEADER_BYTES"),
            32 * 1024,
        )?;
        let max_connections =
            parse_usize(text(&mut lookup, "AJIASU_GATEWAY_MAX_CONNECTIONS"), 1000)?;
        let shutdown_timeout = text(&mut lookup, "AJIASU_GATEWAY_SHUTDOWN_TIMEOUT")
            .map(|value| parse_seconds(&value))
            .transpose()?
            .unwrap_or(Duration::from_secs(30));
        let enrollment = if state_directory.join("session.json").is_file() {
            None
        } else {
            load_enrollment(&mut lookup)?
        };
        let config = Self {
            gateway_name,
            http_listen,
            socks5_listen,
            control_endpoint,
            state_directory,
            enrollment,
            max_header_bytes,
            max_connections,
            shutdown_timeout,
        };
        config.validate()?;
        Ok(config)
    }

    pub fn validate(&self) -> Result<(), ConfigError> {
        if self.http_listen == self.socks5_listen {
            return Err(ConfigError::DuplicateListeners);
        }
        if self.control_endpoint.trim().is_empty() {
            return Err(ConfigError::MissingControlEndpoint);
        }
        if !(1024..=16 * 1024 * 1024).contains(&self.max_header_bytes)
            || self.max_connections == 0
            || self.max_connections > 100_000
        {
            return Err(ConfigError::InvalidBounds);
        }
        Ok(())
    }
}

fn load_enrollment<F>(lookup: &mut F) -> Result<Option<EnrollmentSecret>, ConfigError>
where
    F: FnMut(&str) -> Option<OsString>,
{
    let direct = text(lookup, "AJIASU_GATEWAY_ENROLLMENT_TOKEN").filter(|value| !value.is_empty());
    let file = lookup("AJIASU_GATEWAY_ENROLLMENT_TOKEN_FILE")
        .filter(|value| !value.is_empty())
        .map(PathBuf::from);
    if direct.is_some() && file.is_some() {
        return Err(ConfigError::EnrollmentConflict);
    }
    if let Some(path) = file {
        let content = private_file::read(&path, 1, 64 * 1024)
            .and_then(private_file::trim_nonempty)
            .map_err(|_| ConfigError::InvalidEnrollmentFile)?;
        return Ok(Some(EnrollmentSecret {
            value: SecretBytes::new(content),
            source_path: Some(path),
        }));
    }
    Ok(direct.map(|value| EnrollmentSecret {
        value: SecretBytes::new(value.into_bytes()),
        source_path: None,
    }))
}

fn text<F>(lookup: &mut F, name: &str) -> Option<String>
where
    F: FnMut(&str) -> Option<OsString>,
{
    lookup(name)
        .and_then(|value| value.into_string().ok())
        .map(|value| value.trim().to_owned())
}

fn parse_address(value: &str) -> Result<SocketAddr, ConfigError> {
    value.parse().map_err(|_| ConfigError::InvalidListener)
}

fn parse_usize(value: Option<String>, default: usize) -> Result<usize, ConfigError> {
    value
        .map(|value| value.parse().map_err(|_| ConfigError::InvalidBounds))
        .unwrap_or(Ok(default))
}

fn parse_seconds(value: &str) -> Result<Duration, ConfigError> {
    value
        .strip_suffix('s')
        .and_then(|value| value.parse::<u64>().ok())
        .filter(|seconds| (1..=300).contains(seconds))
        .map(Duration::from_secs)
        .ok_or(ConfigError::InvalidShutdownTimeout)
}

fn container_absolute(path: &std::path::Path) -> bool {
    path.is_absolute() || path.as_os_str().to_string_lossy().starts_with('/')
}

#[cfg(test)]
mod tests {
    use std::{collections::HashMap, fs};

    use super::*;

    fn valid_environment() -> (HashMap<String, OsString>, PathBuf) {
        let root =
            std::env::temp_dir().join(format!("ajiasu-gateway-config-{}", uuid::Uuid::now_v7()));
        fs::create_dir_all(&root).unwrap();
        let token = root.join("enrollment");
        private_file::atomic_write(&token, b"gateway-token\n").unwrap();
        (
            HashMap::from([
                (
                    "AJIASU_GATEWAY_NAME".to_owned(),
                    OsString::from("gateway-a"),
                ),
                (
                    "AJIASU_GATEWAY_CONTROL_PLANE_ENDPOINT".to_owned(),
                    OsString::from("http://control-plane:9091"),
                ),
                (
                    "AJIASU_GATEWAY_STATE_DIRECTORY".to_owned(),
                    root.clone().into_os_string(),
                ),
                (
                    "AJIASU_GATEWAY_ENROLLMENT_TOKEN_FILE".to_owned(),
                    token.into_os_string(),
                ),
                (
                    "AJIASU_GATEWAY_SHUTDOWN_TIMEOUT".to_owned(),
                    OsString::from("20s"),
                ),
            ]),
            root,
        )
    }

    #[test]
    fn loads_file_backed_enrollment_and_graceful_timeout() {
        let (values, root) = valid_environment();
        let config = GatewayConfig::from_lookup(|name| values.get(name).cloned()).unwrap();
        assert_eq!(config.shutdown_timeout, Duration::from_secs(20));
        assert_eq!(config.enrollment.unwrap().value.expose(), b"gateway-token");
        fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn rejects_conflict_and_unsafe_files_without_exposing_values() {
        let (mut values, root) = valid_environment();
        values.insert(
            "AJIASU_GATEWAY_ENROLLMENT_TOKEN".to_owned(),
            OsString::from("gateway-canary"),
        );
        let error = GatewayConfig::from_lookup(|name| values.get(name).cloned()).unwrap_err();
        assert_eq!(error, ConfigError::EnrollmentConflict);
        assert!(!error.to_string().contains("gateway-canary"));
        values.remove("AJIASU_GATEWAY_ENROLLMENT_TOKEN");
        let whitespace = root.join("whitespace");
        private_file::atomic_write(&whitespace, b" \r\n\t").unwrap();
        values.insert(
            "AJIASU_GATEWAY_ENROLLMENT_TOKEN_FILE".to_owned(),
            whitespace.into_os_string(),
        );
        assert_eq!(
            GatewayConfig::from_lookup(|name| values.get(name).cloned()).unwrap_err(),
            ConfigError::InvalidEnrollmentFile
        );
        fs::remove_dir_all(root).unwrap();
    }

    #[cfg(unix)]
    #[test]
    fn rejects_symlink_and_broad_permissions() {
        use std::os::unix::fs::{PermissionsExt, symlink};
        let (mut values, root) = valid_environment();
        let target = root.join("target");
        private_file::atomic_write(&target, b"token").unwrap();
        let link = root.join("link");
        symlink(&target, &link).unwrap();
        values.insert(
            "AJIASU_GATEWAY_ENROLLMENT_TOKEN_FILE".to_owned(),
            link.into_os_string(),
        );
        assert_eq!(
            GatewayConfig::from_lookup(|name| values.get(name).cloned()).unwrap_err(),
            ConfigError::InvalidEnrollmentFile
        );
        fs::set_permissions(&target, fs::Permissions::from_mode(0o640)).unwrap();
        fs::remove_dir_all(root).unwrap();
    }
}
