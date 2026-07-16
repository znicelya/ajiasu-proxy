use std::{
    collections::{BTreeMap, HashMap},
    io::Cursor,
    path::Path,
};

use async_trait::async_trait;
use bollard::{
    Docker,
    container::{
        Config, CreateContainerOptions, ListContainersOptions, RemoveContainerOptions,
        StartContainerOptions, UploadToContainerOptions,
    },
    exec::{CreateExecOptions, StartExecOptions},
    models::HostConfig,
};
use uuid::Uuid;

use super::{RunnerRecord, RunnerSpec, RunnerState, Runtime, RuntimeError};
use crate::relay::RunnerSockets;

const OWNER_LABEL: &str = "ajiasu.owner";
const OWNER_VALUE: &str = "control-plane";

pub struct DockerRuntime {
    docker: Docker,
    node_id: Uuid,
    runner_image: String,
    runner_sockets: RunnerSockets,
    relay_directory: std::path::PathBuf,
}

impl DockerRuntime {
    pub fn connect(
        node_id: Uuid,
        runner_image: String,
        socket: &Path,
        runner_sockets: RunnerSockets,
        relay_directory: std::path::PathBuf,
    ) -> Result<Self, RuntimeError> {
        if node_id.is_nil() || !runner_image.contains("@sha256:") {
            return Err(RuntimeError::Conflict);
        }
        let socket = socket.to_str().ok_or(RuntimeError::Conflict)?;
        let docker = Docker::connect_with_socket(socket, 120, bollard::API_DEFAULT_VERSION)
            .map_err(|_| RuntimeError::Unavailable)?;
        Ok(Self {
            docker,
            node_id,
            runner_image,
            runner_sockets,
            relay_directory,
        })
    }
    async fn owned_containers(
        &self,
    ) -> Result<Vec<bollard::models::ContainerSummary>, RuntimeError> {
        let mut filters = HashMap::new();
        filters.insert(
            "label".to_owned(),
            vec![
                format!("{OWNER_LABEL}={OWNER_VALUE}"),
                format!("ajiasu.node_id={}", self.node_id),
            ],
        );
        self.docker
            .list_containers(Some(ListContainersOptions {
                all: true,
                filters,
                ..Default::default()
            }))
            .await
            .map_err(|_| RuntimeError::Unavailable)
    }
}

#[async_trait]
impl Runtime for DockerRuntime {
    async fn inventory(&self) -> Result<Vec<RunnerRecord>, RuntimeError> {
        let mut records = Vec::new();
        for container in self.owned_containers().await? {
            let labels = container.labels.unwrap_or_default();
            let runner_id = labels
                .get("ajiasu.runner_id")
                .and_then(|v| Uuid::parse_str(v).ok())
                .ok_or(RuntimeError::Conflict)?;
            let operation_id = labels
                .get("ajiasu.operation_id")
                .and_then(|v| Uuid::parse_str(v).ok())
                .ok_or(RuntimeError::Conflict)?;
            let tenant_id = labels
                .get("ajiasu.tenant_id")
                .and_then(|v| Uuid::parse_str(v).ok())
                .ok_or(RuntimeError::Conflict)?;
            let endpoint_id = labels
                .get("ajiasu.endpoint_id")
                .and_then(|v| Uuid::parse_str(v).ok())
                .ok_or(RuntimeError::Conflict)?;
            let generation = labels
                .get("ajiasu.generation")
                .and_then(|v| v.parse().ok())
                .ok_or(RuntimeError::Conflict)?;
            records.push(RunnerRecord {
                spec: RunnerSpec {
                    runner_id,
                    operation_id,
                    tenant_id,
                    endpoint_id,
                    generation,
                    labels: labels.into_iter().collect::<BTreeMap<_, _>>(),
                },
                state: if container.state.as_deref() == Some("running") {
                    RunnerState::Running
                } else {
                    RunnerState::Stopped
                },
            });
        }
        Ok(records)
    }
    async fn create(
        &self,
        spec: RunnerSpec,
        credential: &[u8],
    ) -> Result<RunnerRecord, RuntimeError> {
        if credential.is_empty() {
            return Err(RuntimeError::Conflict);
        }
        if let Some(existing) = self
            .inventory()
            .await?
            .into_iter()
            .find(|item| item.spec.runner_id == spec.runner_id)
        {
            if existing.spec.generation > spec.generation {
                return Err(RuntimeError::StaleGeneration);
            }
            if existing.spec.generation == spec.generation {
                return Ok(existing);
            }
            self.stop(spec.runner_id, spec.generation).await?;
        }
        let mut labels: HashMap<String, String> = spec.labels.clone().into_iter().collect();
        labels.insert(OWNER_LABEL.to_owned(), OWNER_VALUE.to_owned());
        labels.insert("ajiasu.node_id".to_owned(), self.node_id.to_string());
        labels.insert("ajiasu.runner_id".to_owned(), spec.runner_id.to_string());
        labels.insert("ajiasu.tenant_id".to_owned(), spec.tenant_id.to_string());
        labels.insert(
            "ajiasu.endpoint_id".to_owned(),
            spec.endpoint_id.to_string(),
        );
        labels.insert(
            "ajiasu.operation_id".to_owned(),
            spec.operation_id.to_string(),
        );
        labels.insert("ajiasu.generation".to_owned(), spec.generation.to_string());
        let mut tmpfs = HashMap::new();
        tmpfs.insert(
            "/run/ajiasu".to_owned(),
            "rw,noexec,nosuid,nodev,mode=0700,size=1048576".to_owned(),
        );
        let relay_directory =
            self.relay_directory
                .join(format!("{}-{}", spec.runner_id.simple(), spec.generation));
        std::fs::create_dir_all(&relay_directory).map_err(|_| RuntimeError::Unavailable)?;
        let runner_socket = relay_directory.join("runner.sock");
        let bind = format!("{}:/run/ajiasu-relay:rw", relay_directory.to_string_lossy());
        let host_config = HostConfig {
            network_mode: Some("none".to_owned()),
            readonly_rootfs: Some(true),
            cap_drop: Some(vec!["ALL".to_owned()]),
            security_opt: Some(vec!["no-new-privileges:true".to_owned()]),
            tmpfs: Some(tmpfs),
            binds: Some(vec![bind]),
            memory: Some(256 * 1024 * 1024),
            nano_cpus: Some(1_000_000_000),
            ..Default::default()
        };
        let config = Config {
            image: Some(self.runner_image.clone()),
            user: Some("65532:65532".to_owned()),
            entrypoint: Some(vec!["/bin/sh".to_owned()]),
            cmd: Some(vec!["-c".to_owned(), "sleep 2147483647".to_owned()]),
            labels: Some(labels),
            env: Some(vec![
                "AJIASU_RUNNER_RELAY_SOCKET=/run/ajiasu-relay/runner.sock".to_owned(),
            ]),
            host_config: Some(host_config),
            ..Default::default()
        };
        let name = format!("ajiasu-runner-{}", spec.runner_id.simple());
        self.docker
            .create_container(
                Some(CreateContainerOptions {
                    name: name.clone(),
                    platform: None,
                }),
                config,
            )
            .await
            .map_err(|_| RuntimeError::Unavailable)?;
        self.docker
            .start_container(&name, None::<StartContainerOptions<String>>)
            .await
            .map_err(|_| RuntimeError::Unavailable)?;
        let archive = credential_archive(credential)?;
        self.docker
            .upload_to_container(
                &name,
                Some(UploadToContainerOptions {
                    path: "/run/ajiasu",
                    no_overwrite_dir_non_dir: "true",
                }),
                bytes::Bytes::from(archive),
            )
            .await
            .map_err(|_| RuntimeError::Unavailable)?;
        let exec = self
            .docker
            .create_exec(
                &name,
                CreateExecOptions {
                    cmd: Some(vec!["/usr/local/bin/runner-entrypoint.sh", "connect"]),
                    user: Some("65532:65532"),
                    attach_stdout: Some(false),
                    attach_stderr: Some(false),
                    ..Default::default()
                },
            )
            .await
            .map_err(|_| RuntimeError::Unavailable)?;
        self.docker
            .start_exec(
                &exec.id,
                Some(StartExecOptions {
                    detach: true,
                    tty: false,
                    output_capacity: None,
                }),
            )
            .await
            .map_err(|_| RuntimeError::Unavailable)?;
        self.runner_sockets
            .insert(
                spec.tenant_id,
                spec.endpoint_id,
                spec.runner_id,
                spec.generation,
                runner_socket,
            )
            .map_err(|_| RuntimeError::Unavailable)?;
        Ok(RunnerRecord {
            spec,
            state: RunnerState::Running,
        })
    }
    async fn stop(&self, runner_id: Uuid, generation: u64) -> Result<(), RuntimeError> {
        for item in self.inventory().await? {
            if item.spec.runner_id != runner_id {
                continue;
            }
            if item.spec.generation > generation {
                return Err(RuntimeError::StaleGeneration);
            }
            let name = format!("ajiasu-runner-{}", runner_id.simple());
            self.docker
                .remove_container(
                    &name,
                    Some(RemoveContainerOptions {
                        force: true,
                        ..Default::default()
                    }),
                )
                .await
                .map_err(|_| RuntimeError::Unavailable)?;
            self.runner_sockets.remove(runner_id, item.spec.generation);
            let relay_directory = self.relay_directory.join(format!(
                "{}-{}",
                runner_id.simple(),
                item.spec.generation
            ));
            let _ = std::fs::remove_dir_all(relay_directory);
        }
        Ok(())
    }
    async fn rebuild(
        &self,
        spec: RunnerSpec,
        credential: &[u8],
    ) -> Result<RunnerRecord, RuntimeError> {
        self.stop(spec.runner_id, spec.generation).await?;
        self.create(spec, credential).await
    }
    async fn garbage_collect(&self, desired: &[Uuid]) -> Result<Vec<Uuid>, RuntimeError> {
        let mut removed = Vec::new();
        for item in self.inventory().await? {
            if !desired.contains(&item.spec.runner_id) {
                self.stop(item.spec.runner_id, item.spec.generation).await?;
                removed.push(item.spec.runner_id);
            }
        }
        Ok(removed)
    }
}

fn credential_archive(credential: &[u8]) -> Result<Vec<u8>, RuntimeError> {
    let mut archive = Vec::new();
    {
        let mut builder = tar::Builder::new(&mut archive);
        let mut header = tar::Header::new_gnu();
        header.set_size(credential.len() as u64);
        header.set_mode(0o400);
        header.set_uid(65532);
        header.set_gid(65532);
        header.set_cksum();
        builder
            .append_data(&mut header, "ajiasu.conf", Cursor::new(credential))
            .map_err(|_| RuntimeError::Unavailable)?;
        builder.finish().map_err(|_| RuntimeError::Unavailable)?;
    }
    Ok(archive)
}

#[cfg(test)]
mod tests {
    use super::*;
    use std::io::Read;

    #[test]
    fn credential_archive_is_private_and_contains_only_runtime_config() {
        let archive = credential_archive(b"credential-canary").unwrap();
        let mut reader = tar::Archive::new(Cursor::new(archive));
        let mut entries = reader.entries().unwrap();
        let mut entry = entries.next().unwrap().unwrap();
        assert_eq!(
            entry.path().unwrap().as_ref(),
            std::path::Path::new("ajiasu.conf")
        );
        assert_eq!(entry.header().mode().unwrap(), 0o400);
        assert_eq!(entry.header().uid().unwrap(), 65532);
        let mut content = Vec::new();
        entry.read_to_end(&mut content).unwrap();
        assert_eq!(content, b"credential-canary");
        drop(entry);
        assert!(entries.next().is_none());
    }
}
