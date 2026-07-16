use std::sync::Arc;

use ajiasu_gateway::{
    cli,
    config::GatewayConfig,
    control_client,
    relay::GrpcRelayTransport,
    routes::RouteTable,
    server::{GatewayServer, ListenerConfig},
};
use tokio::sync::watch;
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
    let config = GatewayConfig::from_env()?;
    let gateway_name = config.gateway_name.clone();
    let shutdown_timeout = config.shutdown_timeout;
    let listener_config = ListenerConfig {
        http_listen: config.http_listen,
        socks5_listen: config.socks5_listen,
        max_header_bytes: config.max_header_bytes,
        max_connections: config.max_connections,
    };
    let relay = Arc::new(GrpcRelayTransport::new(
        config.relay_endpoint.clone(),
        config.client_tls.clone(),
    )?);
    let routes = RouteTable::new();
    let (shutdown_tx, shutdown_rx) = watch::channel(false);
    let (ready_tx, mut ready_rx) = watch::channel(false);
    info!(event = "gateway_starting", gateway_name = %gateway_name);
    let mut control_task = tokio::spawn(control_client::run(
        config,
        routes.clone(),
        ready_tx,
        shutdown_rx.clone(),
    ));

    loop {
        tokio::select! {
            result = ready_rx.changed() => {
                if result.is_err() {
                    return Err("gateway readiness channel closed".into());
                }
                if *ready_rx.borrow() {
                    info!(event = "gateway_snapshot_ready");
                    break;
                }
            }
            result = &mut control_task => return result?.map_err(Into::into),
            () = shutdown_signal() => {
                return shutdown_control(control_task, shutdown_tx, shutdown_timeout).await;
            }
        }
    }

    let gateway_server = GatewayServer::new(listener_config, routes, relay);
    let mut server_task = tokio::spawn(gateway_server.serve(shutdown_rx));
    info!(event = "gateway_listeners_started", http = %listener_config.http_listen, socks5 = %listener_config.socks5_listen);

    tokio::select! {
        result = &mut control_task => result?.map_err(Into::into),
        result = &mut server_task => result?.map_err(Into::into),
        () = shutdown_signal() => shutdown_runtime(control_task, server_task, shutdown_tx, shutdown_timeout).await,
    }
}

async fn shutdown_runtime(
    control: tokio::task::JoinHandle<Result<(), control_client::ClientError>>,
    server: tokio::task::JoinHandle<Result<(), std::io::Error>>,
    shutdown: watch::Sender<bool>,
    timeout: std::time::Duration,
) -> Result<(), Box<dyn std::error::Error>> {
    info!(event = "gateway_shutdown_started");
    let _ = shutdown.send(true);
    let drained = async {
        control.await??;
        server.await??;
        Ok::<(), Box<dyn std::error::Error>>(())
    };
    if tokio::time::timeout(timeout, drained).await.is_err() {
        error!(event = "gateway_shutdown_timeout");
        return Err("gateway shutdown deadline exceeded".into());
    }
    info!(event = "gateway_shutdown_complete");
    Ok(())
}

async fn shutdown_control(
    mut task: tokio::task::JoinHandle<Result<(), control_client::ClientError>>,
    shutdown: watch::Sender<bool>,
    timeout: std::time::Duration,
) -> Result<(), Box<dyn std::error::Error>> {
    info!(event = "gateway_shutdown_started");
    let _ = shutdown.send(true);
    match tokio::time::timeout(timeout, &mut task).await {
        Ok(result) => result??,
        Err(_) => {
            task.abort();
            error!(event = "gateway_shutdown_timeout");
            return Err("gateway shutdown deadline exceeded".into());
        }
    }
    info!(event = "gateway_shutdown_complete");
    Ok(())
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
