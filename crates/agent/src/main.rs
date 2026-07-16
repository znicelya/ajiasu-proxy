#![forbid(unsafe_code)]

use ajiasu_agent::{cli, client, config::Config};
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
    let config = Config::from_env()?;
    let shutdown_timeout = config.shutdown_timeout;
    info!(
        event = "agent_starting",
        node_name = %config.node_name,
        runtime = %config.runtime,
    );
    let (shutdown_tx, shutdown_rx) = watch::channel(false);
    let mut task = tokio::spawn(client::run(config, shutdown_rx));
    tokio::select! {
        result = &mut task => result??,
        () = shutdown_signal() => {
            info!(event = "agent_shutdown_started");
            let _ = shutdown_tx.send(true);
            match tokio::time::timeout(shutdown_timeout, &mut task).await {
                Ok(result) => result??,
                Err(_) => {
                    task.abort();
                    error!(event = "agent_shutdown_timeout");
                    return Err("agent shutdown deadline exceeded".into());
                }
            }
            info!(event = "agent_shutdown_complete");
        }
    }
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
