use std::{path::Path, sync::Arc, time::Duration};

use ajiasu_agent_protocol::{
    CURRENT_PROTOCOL_REVISION, PREVIOUS_PROTOCOL_REVISION,
    v1::{
        AgentHello, AgentMessage, Heartbeat, MaintenanceState, RegisterNodeRequest,
        agent_control_client::AgentControlClient, agent_message, control_message,
    },
};
use thiserror::Error;
use tokio::sync::{mpsc, watch};
use tokio_stream::wrappers::ReceiverStream;
use tonic::{
    Request,
    metadata::MetadataValue,
    transport::{Certificate, ClientTlsConfig, Endpoint, Identity},
};
use tracing::{info, warn};
use uuid::Uuid;

use crate::{
    commands,
    config::Config,
    inventory, private_file,
    relay::RunnerSockets,
    runtime::{Runtime, docker::DockerRuntime, process::ProcessRuntime},
    session::{self, SessionState},
};

#[derive(Debug, Error)]
pub enum ClientError {
    #[error("control-plane transport is unavailable")]
    Transport(#[from] tonic::transport::Error),
    #[error("control-plane rejected the agent")]
    Status(#[from] tonic::Status),
    #[error("session state is unavailable")]
    Session(#[from] session::SessionError),
    #[error("enrollment token is required for first registration")]
    MissingEnrollment,
    #[error("agent message channel closed")]
    ChannelClosed,
    #[error("session metadata is invalid")]
    InvalidMetadata,
    #[error("enrollment input could not be retired")]
    EnrollmentCleanup,
}

pub async fn run(
    mut config: Config,
    mut shutdown: watch::Receiver<bool>,
    runner_sockets: RunnerSockets,
) -> Result<(), ClientError> {
    let session_path = config.state_directory.join("session.json");
    let instance_id = load_or_create_instance(&config.state_directory)?;
    let mut state = match session::load(&session_path)? {
        Some(state) => state,
        None => register(&mut config, instance_id, &session_path).await?,
    };
    let runtime: Arc<dyn Runtime> = if config.runtime == "docker" {
        let node_id = Uuid::parse_str(&state.node_id).map_err(|_| ClientError::InvalidMetadata)?;
        Arc::new(
            DockerRuntime::connect(
                node_id,
                config
                    .runner_image
                    .clone()
                    .ok_or(ClientError::InvalidMetadata)?,
                &config.docker_socket,
                runner_sockets,
                config.relay_directory.clone(),
            )
            .map_err(|_| ClientError::InvalidMetadata)?,
        )
    } else {
        Arc::new(ProcessRuntime::new(config.state_directory.clone()))
    };
    loop {
        if *shutdown.borrow() {
            return Ok(());
        }
        match connect_once(
            &config,
            instance_id,
            &mut state,
            runtime.clone(),
            &session_path,
            &mut shutdown,
        )
        .await
        {
            Ok(()) => warn!(event = "agent_stream_closed"),
            Err(error) => warn!(event = "agent_stream_failed", error = %error),
        }
        tokio::select! {
            _ = tokio::time::sleep(Duration::from_secs(2)) => {}
            result = shutdown.changed() => {
                if result.is_err() || *shutdown.borrow() {
                    return Ok(());
                }
            }
        }
    }
}

async fn register(
    config: &mut Config,
    instance_id: Uuid,
    path: &Path,
) -> Result<SessionState, ClientError> {
    let enrollment = config
        .enrollment
        .take()
        .ok_or(ClientError::MissingEnrollment)?;
    let enrollment_token = std::str::from_utf8(enrollment.value.expose())
        .map_err(|_| ClientError::InvalidMetadata)?
        .to_owned();
    let enrollment_path = enrollment.source_path.clone();
    let mut client = connect(config).await?;
    let response = client
        .register_node(RegisterNodeRequest {
            enrollment_token,
            agent_instance_id: instance_id.to_string(),
            requested_node_name: config.node_name.clone(),
            minimum_protocol_revision: PREVIOUS_PROTOCOL_REVISION,
            maximum_protocol_revision: CURRENT_PROTOCOL_REVISION,
            agent_version: config.agent_version.clone(),
            architecture: config.architecture.clone(),
            runtime_capabilities: vec!["process".to_owned()],
        })
        .await?
        .into_inner();
    let state = SessionState {
        node_id: response.node_id,
        session_token: response.session_token,
        protocol_revision: response.selected_protocol_revision,
    };
    session::save(path, &state)?;
    if let Some(path) = enrollment_path
        && let Err(error) = std::fs::remove_file(path)
        && error.kind() != std::io::ErrorKind::NotFound
    {
        return Err(ClientError::EnrollmentCleanup);
    }
    Ok(state)
}

async fn connect_once(
    config: &Config,
    instance_id: Uuid,
    state: &mut SessionState,
    runtime: Arc<dyn Runtime>,
    session_path: &Path,
    shutdown: &mut watch::Receiver<bool>,
) -> Result<(), ClientError> {
    let mut client = connect(config).await?;
    let (tx, rx) = mpsc::channel(64);
    tx.send(AgentMessage {
        body: Some(agent_message::Body::Hello(AgentHello {
            node_id: state.node_id.clone(),
            agent_instance_id: instance_id.to_string(),
            protocol_revision: state.protocol_revision,
            agent_version: config.agent_version.clone(),
            architecture: config.architecture.clone(),
            runtime_capabilities: vec!["process".to_owned()],
        })),
    })
    .await
    .map_err(|_| ClientError::ChannelClosed)?;
    let mut request = Request::new(ReceiverStream::new(rx));
    let authorization = MetadataValue::try_from(format!("Bearer {}", state.session_token))
        .map_err(|_| ClientError::InvalidMetadata)?;
    request
        .metadata_mut()
        .insert("authorization", authorization);
    let mut inbound = client.control_stream(request).await?.into_inner();
    let heartbeat_tx = tx.clone();
    let heartbeat_node = state.node_id.clone();
    let heartbeat_runtime = runtime.clone();
    let mut heartbeat_shutdown = shutdown.clone();
    let heartbeat = tokio::spawn(async move {
        let mut ticker = tokio::time::interval(Duration::from_secs(10));
        loop {
            tokio::select! {
                _ = ticker.tick() => {}
                result = heartbeat_shutdown.changed() => {
                    if result.is_err() || *heartbeat_shutdown.borrow() {
                        break;
                    }
                }
            }
            let records = heartbeat_runtime.inventory().await.unwrap_or_default();
            let active = records.len();
            if heartbeat_tx
                .send(AgentMessage {
                    body: Some(agent_message::Body::Heartbeat(Heartbeat {
                        node_id: heartbeat_node.clone(),
                        observed_at: inventory::now(),
                        observed_labels: Default::default(),
                        maximum_runners: 10,
                        active_runners: active as u32,
                        reserved_headroom: 1,
                        maintenance_state: MaintenanceState::Active as i32,
                    })),
                })
                .await
                .is_err()
            {
                break;
            }
            if heartbeat_tx
                .send(inventory::message(&heartbeat_node, &records))
                .await
                .is_err()
            {
                break;
            }
        }
    });
    info!(event = "agent_stream_connected", node_id = %state.node_id, protocol_revision = state.protocol_revision);
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
            Some(control_message::Body::DesiredInventoryRequest(_)) => {
                let records = runtime.inventory().await.unwrap_or_default();
                tx.send(inventory::message(&state.node_id, &records))
                    .await
                    .map_err(|_| ClientError::ChannelClosed)?;
            }
            Some(control_message::Body::RunnerCommand(command)) => {
                for response in commands::execute(runtime.as_ref(), &state.node_id, command).await {
                    tx.send(response)
                        .await
                        .map_err(|_| ClientError::ChannelClosed)?;
                }
            }
            Some(control_message::Body::SessionRenewal(renewal)) => {
                state.session_token = renewal.session_token;
                session::save(session_path, state)?;
            }
            Some(control_message::Body::MaintenanceCommand(_)) | None => {}
        }
    }
    heartbeat.abort();
    Ok(())
}

async fn connect(
    config: &Config,
) -> Result<AgentControlClient<tonic::transport::Channel>, ClientError> {
    let mut endpoint = Endpoint::from_shared(config.control_plane_endpoint.clone())
        .map_err(|_| ClientError::InvalidMetadata)?;
    if let Some(tls) = &config.control_tls {
        endpoint = endpoint.tls_config(
            ClientTlsConfig::new()
                .ca_certificate(Certificate::from_pem(tls.ca_certificate.clone()))
                .identity(Identity::from_pem(
                    tls.certificate.clone(),
                    tls.private_key.expose(),
                )),
        )?;
    }
    Ok(AgentControlClient::new(endpoint.connect().await?))
}

fn load_or_create_instance(directory: &std::path::Path) -> Result<Uuid, session::SessionError> {
    let path = directory.join("instance-id");
    if let Ok(value) = private_file::read(&path, 1, 128)
        && let Ok(value) = std::str::from_utf8(&value)
        && let Ok(id) = Uuid::parse_str(value.trim())
    {
        return Ok(id);
    }
    let id = Uuid::now_v7();
    private_file::atomic_write(&path, id.to_string().as_bytes())
        .map_err(|_| session::SessionError::Unavailable)?;
    Ok(id)
}
