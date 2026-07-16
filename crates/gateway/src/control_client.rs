use std::time::{Duration, SystemTime};

use ajiasu_gateway_protocol::{
    CURRENT_GATEWAY_PROTOCOL_REVISION, PREVIOUS_GATEWAY_PROTOCOL_REVISION,
    gateway_v1::{
        GatewayHeartbeat, GatewayHello, GatewayMessage, RegisterGatewayRequest, SnapshotAck,
        gateway_control_client::GatewayControlClient, gateway_message,
    },
};
use ed25519_dalek::VerifyingKey;
use thiserror::Error;
use time::{OffsetDateTime, format_description::well_known::Rfc3339};
use tokio::sync::{mpsc, watch};
use tokio_stream::wrappers::ReceiverStream;
use tonic::{
    Request,
    metadata::MetadataValue,
    transport::{Certificate, ClientTlsConfig, Endpoint, Identity},
};
use uuid::Uuid;

use crate::{
    config::GatewayConfig,
    routes::{Credential, Delta, Grant, Route, RouteError, RouteTable, Snapshot},
    session::{self, SessionState},
};

#[derive(Debug, Error)]
pub enum ClientError {
    #[error("gateway control transport is unavailable")]
    Transport(#[from] tonic::transport::Error),
    #[error("gateway control request was rejected")]
    Status(#[from] tonic::Status),
    #[error("gateway session is unavailable")]
    Session(#[from] session::SessionError),
    #[error("gateway enrollment is required")]
    MissingEnrollment,
    #[error("gateway control metadata is invalid")]
    InvalidMetadata,
    #[error("gateway route snapshot is invalid")]
    InvalidSnapshot,
    #[error("gateway control channel is closed")]
    ChannelClosed,
}

pub async fn run(
    mut config: GatewayConfig,
    routes: RouteTable,
    ready: watch::Sender<bool>,
    mut shutdown: watch::Receiver<bool>,
) -> Result<(), ClientError> {
    let session_path = config.state_directory.join("session.json");
    let mut state = match session::load(&session_path)? {
        Some(state) => state,
        None => register(&mut config, &session_path).await?,
    };
    loop {
        if *shutdown.borrow() {
            return Ok(());
        }
        match connect_once(
            &config,
            &mut state,
            &session_path,
            routes.clone(),
            ready.clone(),
            &mut shutdown,
        )
        .await
        {
            Ok(()) if *shutdown.borrow() => return Ok(()),
            Ok(()) | Err(_) => {}
        }
        tokio::select! {
            _ = tokio::time::sleep(Duration::from_secs(2)) => {}
            result = shutdown.changed() => {
                if result.is_err() || *shutdown.borrow() { return Ok(()); }
            }
        }
    }
}

async fn register(
    config: &mut GatewayConfig,
    path: &std::path::Path,
) -> Result<SessionState, ClientError> {
    let enrollment = config
        .enrollment
        .take()
        .ok_or(ClientError::MissingEnrollment)?;
    let token = std::str::from_utf8(enrollment.value.expose())
        .map_err(|_| ClientError::InvalidMetadata)?
        .to_owned();
    let instance_id = Uuid::now_v7();
    let mut client = connect(config).await?;
    let mut request = Request::new(RegisterGatewayRequest {
        enrollment_token: token,
        gateway_instance_id: instance_id.to_string(),
        requested_gateway_name: config.gateway_name.clone(),
        minimum_protocol_revision: PREVIOUS_GATEWAY_PROTOCOL_REVISION,
        maximum_protocol_revision: CURRENT_GATEWAY_PROTOCOL_REVISION,
        gateway_version: env!("CARGO_PKG_VERSION").to_owned(),
        architecture: std::env::consts::ARCH.to_owned(),
        listener_protocols: vec!["http".to_owned(), "connect".to_owned(), "socks5".to_owned()],
    });
    insert_fingerprint(&mut request, &config.certificate_fingerprint)?;
    let response = client.register_gateway(request).await?.into_inner();
    let state = SessionState {
        gateway_id: response.gateway_id,
        gateway_instance_id: instance_id.to_string(),
        session_token: response.session_token,
        protocol_revision: response.selected_protocol_revision,
    };
    session::save(path, &state)?;
    session::retire_enrollment(enrollment.source_path.as_deref())?;
    Ok(state)
}

async fn connect_once(
    config: &GatewayConfig,
    state: &mut SessionState,
    _session_path: &std::path::Path,
    routes: RouteTable,
    ready: watch::Sender<bool>,
    shutdown: &mut watch::Receiver<bool>,
) -> Result<(), ClientError> {
    let gateway_id = parse_uuid(&state.gateway_id).map_err(|_| ClientError::InvalidSnapshot)?;
    let mut client = connect(config).await?;
    let (sender, receiver) = mpsc::channel(64);
    sender
        .send(GatewayMessage {
            body: Some(gateway_message::Body::Hello(GatewayHello {
                gateway_id: state.gateway_id.clone(),
                gateway_instance_id: state.gateway_instance_id.clone(),
                protocol_revision: state.protocol_revision,
                gateway_version: env!("CARGO_PKG_VERSION").to_owned(),
                architecture: std::env::consts::ARCH.to_owned(),
                listener_protocols: vec![
                    "http".to_owned(),
                    "connect".to_owned(),
                    "socks5".to_owned(),
                ],
            })),
        })
        .await
        .map_err(|_| ClientError::ChannelClosed)?;
    let mut request = Request::new(ReceiverStream::new(receiver));
    let authorization = MetadataValue::try_from(format!("Bearer {}", state.session_token))
        .map_err(|_| ClientError::InvalidMetadata)?;
    request
        .metadata_mut()
        .insert("authorization", authorization);
    insert_fingerprint(&mut request, &config.certificate_fingerprint)?;
    let mut inbound = client.control_stream(request).await?.into_inner();
    let heartbeat_sender = sender.clone();
    let mut heartbeat_shutdown = shutdown.clone();
    let heartbeat = tokio::spawn(async move {
        let mut ticker = tokio::time::interval(Duration::from_secs(10));
        loop {
            tokio::select! {
                _ = ticker.tick() => {
                    let observed_at = OffsetDateTime::now_utc().format(&Rfc3339).unwrap_or_default();
                    if heartbeat_sender.send(GatewayMessage { body: Some(gateway_message::Body::Heartbeat(GatewayHeartbeat { observed_at, active_connections: 0, bytes_in: 0, bytes_out: 0 })) }).await.is_err() { break; }
                }
                result = heartbeat_shutdown.changed() => {
                    if result.is_err() || *heartbeat_shutdown.borrow() { break; }
                }
            }
        }
    });
    let mut needs_snapshot = true;
    loop {
        let message = tokio::select! {
            result = inbound.message() => result?,
            result = shutdown.changed() => {
                if result.is_err() || *shutdown.borrow() {
                    heartbeat.abort();
                    return Ok(());
                }
                continue;
            }
        };
        let Some(message) = message else {
            break;
        };
        match message.body {
            Some(ajiasu_gateway_protocol::gateway_v1::control_message::Body::RouteSnapshot(
                snapshot,
            )) => {
                match convert_snapshot(
                    snapshot,
                    &config.route_verifying_key,
                    gateway_id,
                    SystemTime::now(),
                )
                .and_then(|snapshot| routes.apply_snapshot(snapshot))
                {
                    Ok(()) => {
                        needs_snapshot = false;
                        let _ = ready.send(true);
                        sender
                            .send(GatewayMessage {
                                body: Some(gateway_message::Body::SnapshotAck(SnapshotAck {
                                    snapshot_version: routes.version(),
                                    applied_route_count: u32::try_from(routes.route_count())
                                        .unwrap_or(u32::MAX),
                                    failure_code: String::new(),
                                })),
                            })
                            .await
                            .map_err(|_| ClientError::ChannelClosed)?;
                    }
                    Err(_) => return Err(ClientError::InvalidSnapshot),
                }
            }
            Some(ajiasu_gateway_protocol::gateway_v1::control_message::Body::RouteDelta(delta))
                if !needs_snapshot =>
            {
                let converted = convert_route(delta.route.ok_or(ClientError::InvalidSnapshot)?)
                    .and_then(|route| {
                        verify_route(
                            route,
                            &config.route_verifying_key,
                            gateway_id,
                            SystemTime::now(),
                        )
                    })
                    .map_err(|_| ClientError::InvalidSnapshot)?;
                match routes.apply_delta(Delta {
                    version: delta.snapshot_version,
                    route: converted,
                    revoked: delta.revoked,
                }) {
                    Ok(()) | Err(RouteError::StaleVersion | RouteError::StaleAssignment) => {}
                    Err(RouteError::SnapshotRequired) => {
                        needs_snapshot = true;
                        let _ = ready.send(false);
                        sender
                            .send(GatewayMessage {
                                body: Some(gateway_message::Body::SnapshotAck(SnapshotAck {
                                    snapshot_version: routes.version(),
                                    applied_route_count: u32::try_from(routes.route_count())
                                        .unwrap_or(u32::MAX),
                                    failure_code: "snapshot_required".to_owned(),
                                })),
                            })
                            .await
                            .map_err(|_| ClientError::ChannelClosed)?;
                    }
                    Err(_) => return Err(ClientError::InvalidSnapshot),
                }
            }
            Some(ajiasu_gateway_protocol::gateway_v1::control_message::Body::Shutdown(_)) => {
                heartbeat.abort();
                return Ok(());
            }
            _ => {}
        }
    }
    heartbeat.abort();
    Ok(())
}

async fn connect(
    config: &GatewayConfig,
) -> Result<GatewayControlClient<tonic::transport::Channel>, ClientError> {
    let mut endpoint = Endpoint::from_shared(config.control_endpoint.clone())
        .map_err(|_| ClientError::InvalidMetadata)?;
    if let Some(tls) = &config.client_tls {
        endpoint = endpoint.tls_config(
            ClientTlsConfig::new()
                .ca_certificate(Certificate::from_pem(tls.ca_certificate.clone()))
                .identity(Identity::from_pem(
                    tls.certificate.clone(),
                    tls.private_key.expose(),
                )),
        )?;
    }
    Ok(GatewayControlClient::new(endpoint.connect().await?))
}

fn insert_fingerprint<T>(request: &mut Request<T>, fingerprint: &str) -> Result<(), ClientError> {
    let value = MetadataValue::try_from(fingerprint).map_err(|_| ClientError::InvalidMetadata)?;
    request
        .metadata_mut()
        .insert("x-ajiasu-certificate-fingerprint", value);
    Ok(())
}

fn convert_snapshot(
    snapshot: ajiasu_gateway_protocol::gateway_v1::RouteSnapshot,
    key: &VerifyingKey,
    gateway_id: Uuid,
    now: SystemTime,
) -> Result<Snapshot, RouteError> {
    let routes = snapshot
        .routes
        .into_iter()
        .map(|route| {
            convert_route(route).and_then(|route| verify_route(route, key, gateway_id, now))
        })
        .collect::<Result<Vec<_>, _>>()?;
    Ok(Snapshot {
        version: snapshot.snapshot_version,
        routes,
    })
}

fn verify_route(
    route: Route,
    key: &VerifyingKey,
    gateway_id: Uuid,
    now: SystemTime,
) -> Result<Route, RouteError> {
    route.grant.verify(key, gateway_id, now)?;
    Ok(route)
}

fn convert_route(route: ajiasu_gateway_protocol::gateway_v1::Route) -> Result<Route, RouteError> {
    let grant = route.grant.ok_or(RouteError::StaleGrant)?;
    let credentials = route
        .credentials
        .into_iter()
        .map(|credential| {
            Ok(Credential {
                id: parse_uuid(&credential.credential_id)?,
                public_identifier: credential.public_identifier,
                verifier: credential.verifier,
                expires_at: if credential.expires_at.is_empty() {
                    None
                } else {
                    Some(parse_time(&credential.expires_at)?)
                },
                revoked: credential.revoked,
            })
        })
        .collect::<Result<Vec<_>, RouteError>>()?;
    Ok(Route {
        tenant_id: parse_uuid(&route.tenant_id)?,
        endpoint_id: parse_uuid(&route.endpoint_id)?,
        policy_hash: route.policy_hash,
        protocols: route.protocols,
        runner_id: parse_uuid(&grant.runner_id)?,
        generation: grant.generation,
        assignment_id: parse_uuid(&route.assignment_id)?,
        assignment_generation: route.assignment_generation,
        account_id: parse_uuid(&route.account_id)?,
        node_id: parse_uuid(&route.node_id)?,
        assignment_state: route.assignment_state,
        valid_until: parse_time(&route.valid_until)?,
        grant: Grant {
            gateway_id: parse_uuid(&grant.gateway_id)?,
            tenant_id: parse_uuid(&grant.tenant_id)?,
            endpoint_id: parse_uuid(&grant.endpoint_id)?,
            runner_id: parse_uuid(&grant.runner_id)?,
            generation: grant.generation,
            policy_hash: grant.policy_hash,
            expires_at: parse_time(&grant.expires_at)?,
            protocols: grant.protocols,
            signature: grant.signature,
        },
        credentials,
    })
}

fn parse_uuid(value: &str) -> Result<Uuid, RouteError> {
    Uuid::parse_str(value).map_err(|_| RouteError::StaleGrant)
}

fn parse_time(value: &str) -> Result<SystemTime, RouteError> {
    let parsed = OffsetDateTime::parse(value, &Rfc3339).map_err(|_| RouteError::StaleGrant)?;
    let seconds = parsed.unix_timestamp();
    if seconds < 0 {
        return Err(RouteError::StaleGrant);
    }
    Ok(SystemTime::UNIX_EPOCH
        + Duration::from_secs(seconds as u64)
        + Duration::from_nanos(parsed.nanosecond() as u64))
}

#[cfg(test)]
mod tests {
    use super::*;
    use ajiasu_gateway_protocol::gateway_v1;

    #[test]
    fn converts_current_snapshot_and_rejects_bad_metadata() {
        let now = OffsetDateTime::now_utc() + time::Duration::minutes(1);
        let expires = now.format(&Rfc3339).unwrap();
        let gateway = Uuid::now_v7();
        let runner = Uuid::now_v7();
        let route = gateway_v1::Route {
            tenant_id: Uuid::now_v7().to_string(),
            endpoint_id: Uuid::now_v7().to_string(),
            policy_hash: "hash".to_owned(),
            protocols: vec!["connect".to_owned()],
            credentials: vec![],
            grant: Some(gateway_v1::RouteGrant {
                gateway_id: gateway.to_string(),
                tenant_id: Uuid::now_v7().to_string(),
                endpoint_id: Uuid::now_v7().to_string(),
                runner_id: runner.to_string(),
                generation: 1,
                protocols: vec!["connect".to_owned()],
                policy_hash: "hash".to_owned(),
                expires_at: expires.clone(),
                signature: vec![1],
            }),
            assignment_id: Uuid::now_v7().to_string(),
            assignment_generation: 1,
            account_id: Uuid::now_v7().to_string(),
            node_id: Uuid::now_v7().to_string(),
            assignment_state: "assigned".to_owned(),
            valid_until: expires,
        };
        assert!(convert_route(route).is_ok());
        assert!(parse_uuid("invalid").is_err());
    }
}
