use std::net::SocketAddr;

use thiserror::Error;

#[derive(Clone, Debug)]
pub struct GatewayConfig {
    pub http_listen: SocketAddr,
    pub socks5_listen: SocketAddr,
    pub control_endpoint: String,
    pub max_header_bytes: usize,
    pub max_connections: usize,
}

#[derive(Debug, Error, Eq, PartialEq)]
pub enum ConfigError {
    #[error("gateway listener addresses must be distinct")]
    DuplicateListeners,
    #[error("gateway control endpoint is required")]
    MissingControlEndpoint,
    #[error("gateway bounds are invalid")]
    InvalidBounds,
}

impl GatewayConfig {
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
