use ajiasu_gateway_protocol::{
    gateway_v1,
    relay_v1::{
        RelayData, RelayFrame, RelayHalfClose, RelayOpen, relay_frame,
        runner_relay_client::RunnerRelayClient,
    },
};
use async_trait::async_trait;
use prost::Message;
use thiserror::Error;
use time::{OffsetDateTime, format_description::well_known::Rfc3339};
use tokio::sync::mpsc;
use tokio_stream::wrappers::ReceiverStream;
use tonic::Streaming;
use tonic::transport::{Certificate, ClientTlsConfig, Endpoint, Identity};
use uuid::Uuid;

use crate::config::ClientTlsMaterial;
use crate::routes::Route;

#[derive(Debug, Error)]
pub enum RelayError {
    #[error("relay unavailable")]
    Unavailable,
    #[error("relay stream failed")]
    Failed,
}

pub struct RelayConnection {
    sender: mpsc::Sender<RelayFrame>,
    inbound: Streaming<RelayFrame>,
}

#[derive(Clone)]
pub struct RelaySender {
    sender: mpsc::Sender<RelayFrame>,
}

pub struct RelayReceiver {
    inbound: Streaming<RelayFrame>,
}

impl RelayConnection {
    pub fn split(self) -> (RelaySender, RelayReceiver) {
        (
            RelaySender {
                sender: self.sender,
            },
            RelayReceiver {
                inbound: self.inbound,
            },
        )
    }

    pub async fn send(&self, payload: Vec<u8>) -> Result<(), RelayError> {
        if payload.len() > 64 * 1024 {
            return Err(RelayError::Failed);
        }
        self.sender
            .send(RelayFrame {
                body: Some(relay_frame::Body::Data(RelayData { payload })),
            })
            .await
            .map_err(|_| RelayError::Unavailable)
    }

    pub async fn half_close(&self) -> Result<(), RelayError> {
        self.sender
            .send(RelayFrame {
                body: Some(relay_frame::Body::HalfClose(RelayHalfClose {})),
            })
            .await
            .map_err(|_| RelayError::Unavailable)
    }

    pub async fn recv(&mut self) -> Result<Option<Vec<u8>>, RelayError> {
        let Some(frame) = self
            .inbound
            .message()
            .await
            .map_err(|_| RelayError::Unavailable)?
        else {
            return Ok(None);
        };
        match frame.body {
            Some(relay_frame::Body::Data(data)) if data.payload.len() <= 64 * 1024 => {
                Ok(Some(data.payload))
            }
            Some(relay_frame::Body::HalfClose(_)) => Ok(None),
            Some(relay_frame::Body::Error(_)) | None => Err(RelayError::Failed),
            _ => Err(RelayError::Failed),
        }
    }
}

impl RelaySender {
    pub async fn send(&self, payload: Vec<u8>) -> Result<(), RelayError> {
        if payload.len() > 64 * 1024 {
            return Err(RelayError::Failed);
        }
        self.sender
            .send(RelayFrame {
                body: Some(relay_frame::Body::Data(RelayData { payload })),
            })
            .await
            .map_err(|_| RelayError::Unavailable)
    }

    pub async fn half_close(&self) -> Result<(), RelayError> {
        self.sender
            .send(RelayFrame {
                body: Some(relay_frame::Body::HalfClose(RelayHalfClose {})),
            })
            .await
            .map_err(|_| RelayError::Unavailable)
    }
}

impl RelayReceiver {
    pub async fn recv(&mut self) -> Result<Option<Vec<u8>>, RelayError> {
        let Some(frame) = self
            .inbound
            .message()
            .await
            .map_err(|_| RelayError::Unavailable)?
        else {
            return Ok(None);
        };
        match frame.body {
            Some(relay_frame::Body::Data(data)) if data.payload.len() <= 64 * 1024 => {
                Ok(Some(data.payload))
            }
            Some(relay_frame::Body::HalfClose(_)) => Ok(None),
            _ => Err(RelayError::Failed),
        }
    }
}

#[async_trait]
pub trait RelayTransport: Send + Sync {
    async fn open(
        &self,
        route: &Route,
        host: &str,
        port: u16,
        protocol: &str,
    ) -> Result<RelayConnection, RelayError>;
}

#[derive(Clone)]
pub struct GrpcRelayTransport {
    endpoint: String,
    tls: Option<std::sync::Arc<ClientTlsMaterial>>,
}

impl GrpcRelayTransport {
    pub fn new(
        endpoint: String,
        tls: Option<std::sync::Arc<ClientTlsMaterial>>,
    ) -> Result<Self, RelayError> {
        if endpoint.trim().is_empty() {
            return Err(RelayError::Unavailable);
        }
        Ok(Self { endpoint, tls })
    }
}

#[async_trait]
impl RelayTransport for GrpcRelayTransport {
    async fn open(
        &self,
        route: &Route,
        host: &str,
        port: u16,
        protocol: &str,
    ) -> Result<RelayConnection, RelayError> {
        if host.is_empty() || port == 0 || !route.protocols.iter().any(|item| item == protocol) {
            return Err(RelayError::Failed);
        }
        let grant = gateway_v1::RouteGrant {
            gateway_id: route.grant.gateway_id.to_string(),
            tenant_id: route.grant.tenant_id.to_string(),
            endpoint_id: route.grant.endpoint_id.to_string(),
            runner_id: route.grant.runner_id.to_string(),
            generation: route.grant.generation,
            protocols: route.grant.protocols.clone(),
            policy_hash: route.grant.policy_hash.clone(),
            expires_at: format_system_time(route.grant.expires_at)?,
            signature: route.grant.signature.clone(),
        }
        .encode_to_vec();
        let (sender, receiver) = mpsc::channel(32);
        sender
            .send(RelayFrame {
                body: Some(relay_frame::Body::Open(RelayOpen {
                    signed_route_grant: grant,
                    protocol: protocol.to_owned(),
                    target_host: host.to_owned(),
                    target_port: u32::from(port),
                    dns_mode: "runner".to_owned(),
                    request_nonce: Uuid::now_v7().as_bytes().to_vec(),
                    gateway_id: route.grant.gateway_id.to_string(),
                    runner_id: route.runner_id.to_string(),
                    generation: route.generation,
                    assignment_id: route.assignment_id.to_string(),
                    assignment_generation: route.assignment_generation,
                    assignment_valid_until: format_system_time(route.valid_until)?,
                    policy_hash: route.policy_hash.clone(),
                })),
            })
            .await
            .map_err(|_| RelayError::Unavailable)?;
        let mut endpoint =
            Endpoint::from_shared(self.endpoint.clone()).map_err(|_| RelayError::Unavailable)?;
        if let Some(tls) = &self.tls {
            endpoint = endpoint
                .tls_config(
                    ClientTlsConfig::new()
                        .ca_certificate(Certificate::from_pem(tls.ca_certificate.clone()))
                        .identity(Identity::from_pem(
                            tls.certificate.clone(),
                            tls.private_key.expose(),
                        )),
                )
                .map_err(|_| RelayError::Unavailable)?;
        }
        let channel = endpoint
            .connect()
            .await
            .map_err(|_| RelayError::Unavailable)?;
        let mut client = RunnerRelayClient::new(channel);
        let inbound = client
            .open(ReceiverStream::new(receiver))
            .await
            .map_err(|_| RelayError::Unavailable)?
            .into_inner();
        Ok(RelayConnection { sender, inbound })
    }
}

fn format_system_time(value: std::time::SystemTime) -> Result<String, RelayError> {
    let duration = value
        .duration_since(std::time::SystemTime::UNIX_EPOCH)
        .map_err(|_| RelayError::Failed)?;
    OffsetDateTime::from_unix_timestamp(duration.as_secs() as i64)
        .map_err(|_| RelayError::Failed)?
        .replace_nanosecond(duration.subsec_nanos())
        .map_err(|_| RelayError::Failed)?
        .format(&Rfc3339)
        .map_err(|_| RelayError::Failed)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn rejects_empty_endpoint_and_invalid_time() {
        assert!(GrpcRelayTransport::new(String::new(), None).is_err());
        assert!(format_system_time(std::time::SystemTime::UNIX_EPOCH).is_ok());
    }
}
