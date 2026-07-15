use async_trait::async_trait;
use thiserror::Error;

use crate::routes::Route;

#[derive(Debug, Error, Eq, PartialEq)]
pub enum RelayError {
    #[error("relay unavailable")]
    Unavailable,
    #[error("relay stream failed")]
    Failed,
}
#[async_trait]
pub trait RelayTransport: Send + Sync {
    async fn open(
        &self,
        route: &Route,
        host: &str,
        port: u16,
        protocol: &str,
    ) -> Result<(), RelayError>;
}
