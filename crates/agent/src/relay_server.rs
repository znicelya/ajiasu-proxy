use std::{path::Path, pin::Pin, sync::Arc};

use ajiasu_gateway_protocol::{
    gateway_v1,
    relay_v1::{
        RelayData, RelayError as RelayErrorFrame, RelayFrame, RelayHalfClose, relay_frame,
        runner_relay_server::RunnerRelay,
    },
};
use ajiasu_runner_relay::{Metadata as RunnerMetadata, encode_metadata};
use async_trait::async_trait;
use ed25519_dalek::VerifyingKey;
use prost::Message;
use time::{OffsetDateTime, format_description::well_known::Rfc3339};
use tokio::{
    io::{AsyncRead, AsyncReadExt, AsyncWrite, AsyncWriteExt},
    sync::mpsc,
};
use tokio_stream::wrappers::ReceiverStream;
use tonic::{Request, Response, Status, Streaming};
use uuid::Uuid;

use crate::{
    relay::{RelayOpen, RunnerSockets, validate_frame},
    route_grant::RouteGrant,
};

pub trait RelayIO: AsyncRead + AsyncWrite + Unpin + Send {}
impl<T: AsyncRead + AsyncWrite + Unpin + Send> RelayIO for T {}

#[async_trait]
pub trait RunnerConnector: Send + Sync {
    async fn connect(&self, path: &Path) -> Result<Box<dyn RelayIO>, ()>;
}

pub struct SystemRunnerConnector;

#[async_trait]
impl RunnerConnector for SystemRunnerConnector {
    async fn connect(&self, path: &Path) -> Result<Box<dyn RelayIO>, ()> {
        #[cfg(unix)]
        {
            tokio::net::UnixStream::connect(path)
                .await
                .map(|stream| Box::new(stream) as Box<dyn RelayIO>)
                .map_err(|_| ())
        }
        #[cfg(not(unix))]
        {
            let _ = path;
            Err(())
        }
    }
}

#[derive(Clone)]
pub struct RelayService {
    sockets: RunnerSockets,
    verifying_key: VerifyingKey,
    connector: Arc<dyn RunnerConnector>,
}

impl RelayService {
    pub fn new(
        sockets: RunnerSockets,
        verifying_key: VerifyingKey,
        connector: Arc<dyn RunnerConnector>,
    ) -> Self {
        Self {
            sockets,
            verifying_key,
            connector,
        }
    }
}

#[tonic::async_trait]
impl RunnerRelay for RelayService {
    type OpenStream = Pin<Box<dyn tokio_stream::Stream<Item = Result<RelayFrame, Status>> + Send>>;

    async fn open(
        &self,
        request: Request<Streaming<RelayFrame>>,
    ) -> Result<Response<Self::OpenStream>, Status> {
        let mut inbound = request.into_inner();
        let first = inbound
            .message()
            .await?
            .ok_or_else(|| Status::invalid_argument("relay open is required"))?;
        let open = first
            .body
            .and_then(|body| match body {
                relay_frame::Body::Open(open) => Some(open),
                _ => None,
            })
            .ok_or_else(|| Status::invalid_argument("relay open is required"))?;
        if open.request_nonce.is_empty() || open.request_nonce.len() > 64 {
            return Err(Status::invalid_argument("relay metadata is invalid"));
        }
        let authorized =
            convert_open(open).map_err(|_| Status::permission_denied("relay unauthorized"))?;
        let path = self
            .sockets
            .authorize(&authorized, &self.verifying_key, OffsetDateTime::now_utc())
            .map_err(|_| Status::permission_denied("relay unauthorized"))?;
        let mut runner = self
            .connector
            .connect(&path)
            .await
            .map_err(|_| Status::unavailable("runner unavailable"))?;
        write_runner_metadata(runner.as_mut(), &authorized)
            .await
            .map_err(|_| Status::unavailable("runner unavailable"))?;
        let (mut runner_read, mut runner_write) = tokio::io::split(runner);
        let (sender, receiver) = mpsc::channel(32);
        tokio::spawn(async move {
            let mut buffer = vec![0_u8; 64 * 1024];
            loop {
                tokio::select! {
                    message = inbound.message() => {
                        match message {
                            Ok(Some(RelayFrame { body: Some(relay_frame::Body::Data(data)) })) => {
                                if validate_frame(&data.payload).is_err() || runner_write.write_all(&data.payload).await.is_err() {
                                    let _ = send_error(&sender, "relay_failed").await;
                                    break;
                                }
                            }
                            Ok(Some(RelayFrame { body: Some(relay_frame::Body::HalfClose(_)) })) => {
                                let _ = runner_write.shutdown().await;
                            }
                            Ok(Some(_)) => {
                                let _ = send_error(&sender, "invalid_frame").await;
                                break;
                            }
                            Ok(None) => {
                                let _ = runner_write.shutdown().await;
                                break;
                            }
                            Err(_) => break,
                        }
                    }
                    read = runner_read.read(&mut buffer) => {
                        match read {
                            Ok(0) => {
                                let _ = sender.send(Ok(RelayFrame { body: Some(relay_frame::Body::HalfClose(RelayHalfClose {})) })).await;
                                break;
                            }
                            Ok(length) => {
                                if sender.send(Ok(RelayFrame { body: Some(relay_frame::Body::Data(RelayData { payload: buffer[..length].to_vec() })) })).await.is_err() { break; }
                            }
                            Err(_) => {
                                let _ = send_error(&sender, "runner_unavailable").await;
                                break;
                            }
                        }
                    }
                }
            }
        });
        Ok(Response::new(Box::pin(ReceiverStream::new(receiver))))
    }
}

async fn write_runner_metadata(runner: &mut dyn RelayIO, open: &RelayOpen) -> Result<(), ()> {
    let frame = encode_metadata(&RunnerMetadata {
        protocol: open.protocol.clone(),
        target_host: open.target_host.clone(),
        target_port: open.target_port,
        dns_mode: open.dns_mode.clone(),
    })
    .map_err(|_| ())?;
    runner.write_all(&frame).await.map_err(|_| ())
}

async fn send_error(
    sender: &mpsc::Sender<Result<RelayFrame, Status>>,
    code: &str,
) -> Result<(), ()> {
    sender
        .send(Ok(RelayFrame {
            body: Some(relay_frame::Body::Error(RelayErrorFrame {
                code: code.to_owned(),
            })),
        }))
        .await
        .map_err(|_| ())
}

fn convert_open(open: ajiasu_gateway_protocol::relay_v1::RelayOpen) -> Result<RelayOpen, ()> {
    let grant =
        gateway_v1::RouteGrant::decode(open.signed_route_grant.as_slice()).map_err(|_| ())?;
    Ok(RelayOpen {
        gateway_id: parse_uuid(&open.gateway_id)?,
        runner_id: parse_uuid(&open.runner_id)?,
        generation: open.generation,
        assignment_id: parse_uuid(&open.assignment_id)?,
        assignment_generation: open.assignment_generation,
        assignment_valid_until: parse_time(&open.assignment_valid_until)?,
        protocol: open.protocol,
        dns_mode: open.dns_mode,
        policy_hash: open.policy_hash,
        target_host: open.target_host,
        target_port: u16::try_from(open.target_port).map_err(|_| ())?,
        grant: RouteGrant {
            gateway_id: parse_uuid(&grant.gateway_id)?,
            tenant_id: parse_uuid(&grant.tenant_id)?,
            endpoint_id: parse_uuid(&grant.endpoint_id)?,
            runner_id: parse_uuid(&grant.runner_id)?,
            generation: grant.generation,
            protocols: grant.protocols,
            policy_hash: grant.policy_hash,
            expires_at: parse_time(&grant.expires_at)?,
            signature: grant.signature,
        },
    })
}

fn parse_uuid(value: &str) -> Result<Uuid, ()> {
    Uuid::parse_str(value).map_err(|_| ())
}

fn parse_time(value: &str) -> Result<OffsetDateTime, ()> {
    OffsetDateTime::parse(value, &Rfc3339).map_err(|_| ())
}

#[cfg(test)]
mod tests {
    use super::*;
    use ed25519_dalek::{Signer, SigningKey};
    use tokio::io::duplex;

    struct DuplexConnector(tokio::sync::Mutex<Option<tokio::io::DuplexStream>>);

    #[async_trait]
    impl RunnerConnector for DuplexConnector {
        async fn connect(&self, _: &Path) -> Result<Box<dyn RelayIO>, ()> {
            self.0
                .lock()
                .await
                .take()
                .map(|stream| Box::new(stream) as Box<dyn RelayIO>)
                .ok_or(())
        }
    }

    #[tokio::test]
    async fn converts_and_writes_signed_open_metadata() {
        let signing = SigningKey::from_bytes(&[9_u8; 32]);
        let now = OffsetDateTime::now_utc();
        let mut grant = crate::route_grant::RouteGrant {
            gateway_id: Uuid::now_v7(),
            tenant_id: Uuid::now_v7(),
            endpoint_id: Uuid::now_v7(),
            runner_id: Uuid::now_v7(),
            generation: 3,
            protocols: vec!["connect".to_owned()],
            policy_hash: "hash".to_owned(),
            expires_at: now + time::Duration::minutes(1),
            signature: vec![],
        };
        grant.signature = signing
            .sign(&grant.signing_bytes().unwrap())
            .to_bytes()
            .to_vec();
        let encoded = gateway_v1::RouteGrant {
            gateway_id: grant.gateway_id.to_string(),
            tenant_id: grant.tenant_id.to_string(),
            endpoint_id: grant.endpoint_id.to_string(),
            runner_id: grant.runner_id.to_string(),
            generation: 3,
            protocols: grant.protocols.clone(),
            policy_hash: "hash".to_owned(),
            expires_at: grant.expires_at.format(&Rfc3339).unwrap(),
            signature: grant.signature.clone(),
        }
        .encode_to_vec();
        let open = convert_open(ajiasu_gateway_protocol::relay_v1::RelayOpen {
            signed_route_grant: encoded,
            protocol: "connect".to_owned(),
            target_host: "example.com".to_owned(),
            target_port: 443,
            dns_mode: "runner".to_owned(),
            request_nonce: vec![1],
            gateway_id: grant.gateway_id.to_string(),
            runner_id: grant.runner_id.to_string(),
            generation: 3,
            assignment_id: Uuid::now_v7().to_string(),
            assignment_generation: 3,
            assignment_valid_until: (now + time::Duration::minutes(1)).format(&Rfc3339).unwrap(),
            policy_hash: "hash".to_owned(),
        })
        .unwrap();
        assert_eq!(open.grant.tenant_id, grant.tenant_id);
        let expected = encode_metadata(&RunnerMetadata {
            protocol: "connect".into(),
            target_host: "example.com".into(),
            target_port: 443,
            dns_mode: "runner".into(),
        })
        .unwrap();
        let (mut left, mut right) = duplex(1024);
        write_runner_metadata(&mut left, &open).await.unwrap();
        let mut actual = vec![0; expected.len()];
        right.read_exact(&mut actual).await.unwrap();
        assert_eq!(actual, expected);
        let _connector = DuplexConnector(tokio::sync::Mutex::new(Some(right)));
    }
}
