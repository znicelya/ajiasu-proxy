use std::{
    env,
    ffi::{OsStr, OsString},
    net::SocketAddr,
    path::PathBuf,
    time::Duration,
};

use ed25519_dalek::VerifyingKey;
use thiserror::Error;

use crate::{private_file, secret::SecretBytes};

pub struct EnrollmentSecret {
    pub value: SecretBytes,
    pub source_path: Option<PathBuf>,
}

pub struct ServerTlsMaterial {
    pub certificate: Vec<u8>,
    pub private_key: SecretBytes,
    pub client_ca_certificate: Vec<u8>,
}

impl std::fmt::Debug for ServerTlsMaterial {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        formatter.write_str("ServerTlsMaterial([redacted])")
    }
}

impl std::fmt::Debug for EnrollmentSecret {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        formatter.write_str("EnrollmentSecret([redacted])")
    }
}

#[derive(Debug)]
pub struct Config {
    pub node_name: String,
    pub state_directory: PathBuf,
    pub control_plane_endpoint: String,
    pub enrollment: Option<EnrollmentSecret>,
    pub agent_version: String,
    pub architecture: String,
    pub runtime: String,
    pub runner_image: Option<String>,
    pub docker_socket: PathBuf,
    pub shutdown_timeout: Duration,
    pub relay_bind: SocketAddr,
    pub route_verifying_key: VerifyingKey,
    pub relay_directory: PathBuf,
    pub relay_tls: Option<ServerTlsMaterial>,
}

#[derive(Debug, Error, Eq, PartialEq)]
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
    #[error("AJIASU_AGENT_ENROLLMENT_TOKEN conflicts with AJIASU_AGENT_ENROLLMENT_TOKEN_FILE")]
    EnrollmentConflict,
    #[error("AJIASU_AGENT_ENROLLMENT_TOKEN_FILE is invalid")]
    InvalidEnrollmentFile,
    #[error("AJIASU_DOCKER_SOCKET must be an absolute container path")]
    InvalidDockerSocket,
    #[error("AJIASU_AGENT_SHUTDOWN_TIMEOUT must be between 1s and 300s")]
    InvalidShutdownTimeout,
    #[error("AJIASU_AGENT_RELAY_BIND must be a socket address")]
    InvalidRelayBind,
    #[error("AJIASU_AGENT_ROUTE_VERIFYING_KEY_FILE is invalid")]
    InvalidRouteVerifyingKey,
    #[error("AJIASU_AGENT_RELAY_DIRECTORY must be absolute")]
    InvalidRelayDirectory,
    #[error("agent relay mTLS files are invalid")]
    InvalidRelayTlsFiles,
}

impl Config {
    pub fn from_env() -> Result<Self, ConfigError> {
        Self::from_lookup(|name| env::var_os(name))
    }

    pub fn from_lookup<F>(mut lookup: F) -> Result<Self, ConfigError>
    where
        F: FnMut(&str) -> Option<OsString>,
    {
        let node_name = lookup_text(&mut lookup, "AJIASU_AGENT_NODE_NAME")
            .filter(|value| !value.is_empty())
            .ok_or(ConfigError::MissingNodeName)?;
        let state_directory = lookup("AJIASU_AGENT_STATE_DIRECTORY")
            .map(PathBuf::from)
            .unwrap_or_else(|| PathBuf::from("/var/lib/ajiasu-agent"));
        if !container_absolute(&state_directory) {
            return Err(ConfigError::InvalidStateDirectory);
        }
        let control_plane_endpoint =
            lookup_text(&mut lookup, "AJIASU_AGENT_CONTROL_PLANE_ENDPOINT")
                .filter(|value| !value.is_empty())
                .ok_or(ConfigError::MissingControlPlaneEndpoint)?;
        let runtime =
            lookup_text(&mut lookup, "AJIASU_AGENT_RUNTIME").unwrap_or_else(|| "docker".to_owned());
        if runtime != "docker" && runtime != "process" {
            return Err(ConfigError::InvalidRuntime);
        }
        let runner_image =
            lookup_text(&mut lookup, "AJIASU_AGENT_RUNNER_IMAGE").filter(|value| !value.is_empty());
        if runtime == "docker" && !runner_image.as_deref().is_some_and(immutable_image) {
            return Err(ConfigError::InvalidRunnerImage);
        }
        let docker_socket = lookup("AJIASU_DOCKER_SOCKET")
            .map(PathBuf::from)
            .unwrap_or_else(|| PathBuf::from("/var/run/docker.sock"));
        if runtime == "docker" && !container_absolute(&docker_socket) {
            return Err(ConfigError::InvalidDockerSocket);
        }
        let shutdown_timeout = lookup_text(&mut lookup, "AJIASU_AGENT_SHUTDOWN_TIMEOUT")
            .map(|value| parse_seconds(&value))
            .transpose()?
            .unwrap_or(Duration::from_secs(30));
        let relay_bind = lookup_text(&mut lookup, "AJIASU_AGENT_RELAY_BIND")
            .unwrap_or_else(|| "0.0.0.0:9092".to_owned())
            .parse()
            .map_err(|_| ConfigError::InvalidRelayBind)?;
        let verifying_key_path = lookup("AJIASU_AGENT_ROUTE_VERIFYING_KEY_FILE")
            .map(PathBuf::from)
            .ok_or(ConfigError::InvalidRouteVerifyingKey)?;
        let verifying_key_bytes = private_file::read(&verifying_key_path, 32, 32)
            .map_err(|_| ConfigError::InvalidRouteVerifyingKey)?;
        let route_verifying_key = VerifyingKey::from_bytes(
            verifying_key_bytes
                .as_slice()
                .try_into()
                .map_err(|_| ConfigError::InvalidRouteVerifyingKey)?,
        )
        .map_err(|_| ConfigError::InvalidRouteVerifyingKey)?;
        let relay_directory = lookup("AJIASU_AGENT_RELAY_DIRECTORY")
            .map(PathBuf::from)
            .unwrap_or_else(|| state_directory.join("relay"));
        if !container_absolute(&relay_directory) {
            return Err(ConfigError::InvalidRelayDirectory);
        }
        let relay_certificate = lookup("AJIASU_AGENT_RELAY_CERT_FILE").map(PathBuf::from);
        let relay_key = lookup("AJIASU_AGENT_RELAY_KEY_FILE").map(PathBuf::from);
        let relay_client_ca = lookup("AJIASU_AGENT_RELAY_CLIENT_CA_FILE").map(PathBuf::from);
        let relay_tls = match (relay_certificate, relay_key, relay_client_ca) {
            (None, None, None) => None,
            (Some(certificate), Some(key), Some(client_ca)) => Some(ServerTlsMaterial {
                certificate: private_file::read(&certificate, 1, 4 * 1024 * 1024)
                    .map_err(|_| ConfigError::InvalidRelayTlsFiles)?,
                private_key: SecretBytes::new(
                    private_file::read(&key, 1, 4 * 1024 * 1024)
                        .map_err(|_| ConfigError::InvalidRelayTlsFiles)?,
                ),
                client_ca_certificate: private_file::read(&client_ca, 1, 4 * 1024 * 1024)
                    .map_err(|_| ConfigError::InvalidRelayTlsFiles)?,
            }),
            _ => return Err(ConfigError::InvalidRelayTlsFiles),
        };
        let session_exists = state_directory.join("session.json").is_file();
        let enrollment = if session_exists {
            None
        } else {
            load_enrollment(&mut lookup)?
        };
        Ok(Self {
            node_name,
            state_directory,
            control_plane_endpoint,
            enrollment,
            agent_version: lookup_text(&mut lookup, "AJIASU_AGENT_VERSION")
                .unwrap_or_else(|| env!("CARGO_PKG_VERSION").to_owned()),
            architecture: std::env::consts::ARCH.to_owned(),
            runtime,
            runner_image,
            docker_socket,
            shutdown_timeout,
            relay_bind,
            route_verifying_key,
            relay_directory,
            relay_tls,
        })
    }
}

fn load_enrollment<F>(lookup: &mut F) -> Result<Option<EnrollmentSecret>, ConfigError>
where
    F: FnMut(&str) -> Option<OsString>,
{
    let direct =
        lookup_text(lookup, "AJIASU_AGENT_ENROLLMENT_TOKEN").filter(|value| !value.is_empty());
    let file = lookup("AJIASU_AGENT_ENROLLMENT_TOKEN_FILE")
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

fn lookup_text<F>(lookup: &mut F, name: &str) -> Option<String>
where
    F: FnMut(&str) -> Option<OsString>,
{
    lookup(name)
        .and_then(|value| value.into_string().ok())
        .map(|value| value.trim().to_owned())
}

fn immutable_image(value: &str) -> bool {
    let Some((repository, digest)) = value.rsplit_once("@sha256:") else {
        return false;
    };
    !repository.is_empty()
        && !repository
            .rsplit('/')
            .next()
            .is_some_and(|name| name.contains(':'))
        && digest.len() == 64
        && digest
            .bytes()
            .all(|byte| byte.is_ascii_hexdigit() && !byte.is_ascii_uppercase())
}

fn container_absolute(path: &std::path::Path) -> bool {
    path.is_absolute() || path.as_os_str().to_string_lossy().starts_with('/')
}

fn parse_seconds(value: &str) -> Result<Duration, ConfigError> {
    let seconds = value
        .strip_suffix('s')
        .and_then(|value| value.parse::<u64>().ok())
        .filter(|seconds| (1..=300).contains(seconds))
        .ok_or(ConfigError::InvalidShutdownTimeout)?;
    Ok(Duration::from_secs(seconds))
}

trait OsStringExt {
    fn is_empty(&self) -> bool;
}

impl OsStringExt for OsString {
    fn is_empty(&self) -> bool {
        self.as_os_str() == OsStr::new("")
    }
}

#[cfg(test)]
mod tests {
    use ed25519_dalek::SigningKey;
    use std::{collections::HashMap, fs};

    use super::*;

    fn valid_environment() -> (HashMap<String, OsString>, PathBuf) {
        let root =
            std::env::temp_dir().join(format!("ajiasu-agent-config-{}", uuid::Uuid::now_v7()));
        fs::create_dir_all(&root).unwrap();
        let enrollment = root.join("enrollment-token");
        private_file::atomic_write(&enrollment, b"one-time-token\n").unwrap();
        let verifying_key = root.join("route-verifying-key");
        private_file::atomic_write(
            &verifying_key,
            &SigningKey::from_bytes(&[7_u8; 32])
                .verifying_key()
                .to_bytes(),
        )
        .unwrap();
        let values = HashMap::from([
            (
                "AJIASU_AGENT_NODE_NAME".to_owned(),
                OsString::from("node-a"),
            ),
            (
                "AJIASU_AGENT_STATE_DIRECTORY".to_owned(),
                root.clone().into_os_string(),
            ),
            (
                "AJIASU_AGENT_CONTROL_PLANE_ENDPOINT".to_owned(),
                OsString::from("http://control-plane:9090"),
            ),
            ("AJIASU_AGENT_RUNTIME".to_owned(), OsString::from("docker")),
            (
                "AJIASU_AGENT_RUNNER_IMAGE".to_owned(),
                OsString::from(format!("ghcr.io/example/runner@sha256:{}", "a".repeat(64))),
            ),
            (
                "AJIASU_AGENT_ENROLLMENT_TOKEN_FILE".to_owned(),
                enrollment.into_os_string(),
            ),
            (
                "AJIASU_DOCKER_SOCKET".to_owned(),
                OsString::from("/var/run/docker.sock"),
            ),
            (
                "AJIASU_AGENT_SHUTDOWN_TIMEOUT".to_owned(),
                OsString::from("15s"),
            ),
            (
                "AJIASU_AGENT_RELAY_BIND".to_owned(),
                OsString::from("127.0.0.1:9092"),
            ),
            (
                "AJIASU_AGENT_ROUTE_VERIFYING_KEY_FILE".to_owned(),
                verifying_key.into_os_string(),
            ),
        ]);
        (values, root)
    }

    #[test]
    fn loads_file_backed_enrollment_and_docker_socket() {
        let (values, root) = valid_environment();
        let config = Config::from_lookup(|name| values.get(name).cloned()).unwrap();
        assert_eq!(config.node_name, "node-a");
        assert_eq!(config.docker_socket, PathBuf::from("/var/run/docker.sock"));
        assert_eq!(config.shutdown_timeout, Duration::from_secs(15));
        assert_eq!(config.enrollment.unwrap().value.expose(), b"one-time-token");
        fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn rejects_conflict_without_exposing_token() {
        let (mut values, root) = valid_environment();
        values.insert(
            "AJIASU_AGENT_ENROLLMENT_TOKEN".to_owned(),
            OsString::from("token-canary"),
        );
        let error = Config::from_lookup(|name| values.get(name).cloned()).unwrap_err();
        assert_eq!(error, ConfigError::EnrollmentConflict);
        assert!(!error.to_string().contains("token-canary"));
        fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn rejects_whitespace_oversize_directory_and_symlink() {
        for kind in ["whitespace", "oversize", "directory"] {
            let (mut values, root) = valid_environment();
            let path = root.join(kind);
            match kind {
                "whitespace" => private_file::atomic_write(&path, b" \r\n\t").unwrap(),
                "oversize" => {
                    private_file::atomic_write(&path, &vec![b'x'; 64 * 1024 + 1]).unwrap()
                }
                "directory" => fs::create_dir(&path).unwrap(),
                _ => unreachable!(),
            }
            values.insert(
                "AJIASU_AGENT_ENROLLMENT_TOKEN_FILE".to_owned(),
                path.into_os_string(),
            );
            assert_eq!(
                Config::from_lookup(|name| values.get(name).cloned()).unwrap_err(),
                ConfigError::InvalidEnrollmentFile
            );
            fs::remove_dir_all(root).unwrap();
        }
    }

    #[cfg(unix)]
    #[test]
    fn rejects_enrollment_symlink() {
        use std::os::unix::fs::symlink;
        let (mut values, root) = valid_environment();
        let target = root.join("target");
        private_file::atomic_write(&target, b"token").unwrap();
        let link = root.join("link");
        symlink(target, &link).unwrap();
        values.insert(
            "AJIASU_AGENT_ENROLLMENT_TOKEN_FILE".to_owned(),
            link.into_os_string(),
        );
        assert_eq!(
            Config::from_lookup(|name| values.get(name).cloned()).unwrap_err(),
            ConfigError::InvalidEnrollmentFile
        );
        fs::remove_dir_all(root).unwrap();
    }

    #[test]
    fn durable_session_makes_enrollment_input_unnecessary() {
        let (mut values, root) = valid_environment();
        private_file::atomic_write(
            &root.join("session.json"),
            br#"{"node_id":"node","session_token":"token","protocol_revision":2}"#,
        )
        .unwrap();
        values.remove("AJIASU_AGENT_ENROLLMENT_TOKEN_FILE");
        let config = Config::from_lookup(|name| values.get(name).cloned()).unwrap();
        assert!(config.enrollment.is_none());
        fs::remove_dir_all(root).unwrap();
    }
}
