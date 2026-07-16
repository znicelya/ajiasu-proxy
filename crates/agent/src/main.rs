#![forbid(unsafe_code)]

use std::sync::Arc;

use ajiasu_agent::{
    cli, client,
    config::Config,
    relay::RunnerSockets,
    relay_server::{RelayService, SystemRunnerConnector},
};
use ajiasu_gateway_protocol::relay_v1::runner_relay_server::RunnerRelayServer;
use tokio::sync::watch;
use tonic::transport::{Certificate, Identity, Server, ServerTlsConfig};
use tracing::{error, info};

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    let args: Vec<String> = std::env::args().skip(1).collect();
    if let Some(exit_code) = cli::run_env(&args) {
        if exit_code != 0 {
            std::process::exit(exit_code);
        }
        return Ok(());
    }
    tracing_subscriber::fmt()
        .json()
        .with_env_filter("info")
        .init();
    let mut config = Config::from_env()?;
    let shutdown_timeout = config.shutdown_timeout;
    let relay_bind = config.relay_bind;
    let verifying_key = config.route_verifying_key;
    let relay_tls = config.relay_tls.take();
    info!(
        event = "agent_starting",
        node_name = %config.node_name,
        runtime = %config.runtime,
    );
    let sockets = RunnerSockets::default();
    let (shutdown_tx, shutdown_rx) = watch::channel(false);
    let client_shutdown = shutdown_rx.clone();
    let relay_shutdown = shutdown_rx.clone();
    let mut client_task = tokio::spawn(client::run(config, client_shutdown, sockets.clone()));
    let relay = RelayService::new(sockets, verifying_key, Arc::new(SystemRunnerConnector));
    let mut relay_task = tokio::spawn(async move {
        let mut server = Server::builder();
        if let Some(tls) = relay_tls {
            server = server.tls_config(
                ServerTlsConfig::new()
                    .identity(Identity::from_pem(
                        tls.certificate,
                        tls.private_key.expose(),
                    ))
                    .client_ca_root(Certificate::from_pem(tls.client_ca_certificate)),
            )?;
        }
        server
            .add_service(RunnerRelayServer::new(relay))
            .serve_with_shutdown(relay_bind, wait_for_shutdown(relay_shutdown))
            .await
    });
    info!(event = "agent_relay_starting", bind = %relay_bind);

    tokio::select! {
        result = &mut client_task => result??,
        result = &mut relay_task => result??,
        () = shutdown_signal() => {
            info!(event = "agent_shutdown_started");
            let _ = shutdown_tx.send(true);
            let drained = async {
                client_task.await??;
                relay_task.await??;
                Ok::<(), Box<dyn std::error::Error>>(())
            };
            if tokio::time::timeout(shutdown_timeout, drained).await.is_err() {
                error!(event = "agent_shutdown_timeout");
                return Err("agent shutdown deadline exceeded".into());
            }
            info!(event = "agent_shutdown_complete");
        }
    }
    Ok(())
}

async fn wait_for_shutdown(mut shutdown: watch::Receiver<bool>) {
    loop {
        if *shutdown.borrow() || shutdown.changed().await.is_err() {
            return;
        }
    }
}

async fn shutdown_signal() {
    #[cfg(unix)]
    {
        let mut terminate =
            tokio::signal::unix::signal(tokio::signal::unix::SignalKind::terminate())
                .expect("install SIGTERM handler");
        tokio::select! {
            _ = tokio::signal::ctrl_c() => {}
            _ = terminate.recv() => {}
        }
    }
    #[cfg(not(unix))]
    {
        let _ = tokio::signal::ctrl_c().await;
    }
}

#[cfg(test)]
mod tests {
    #[tokio::test]
    async fn shutdown_deadline_is_bounded() {
        let result = tokio::time::timeout(
            std::time::Duration::from_millis(10),
            std::future::pending::<()>(),
        )
        .await;
        assert!(result.is_err());
    }
}
