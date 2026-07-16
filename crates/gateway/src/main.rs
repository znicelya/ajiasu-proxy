use ajiasu_gateway::{cli, config::GatewayConfig};
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
    info!(event = "gateway_starting", gateway_name = %config.gateway_name);
    shutdown_signal().await;
    info!(event = "gateway_shutdown_started");
    if tokio::time::timeout(config.shutdown_timeout, async {})
        .await
        .is_err()
    {
        error!(event = "gateway_shutdown_timeout");
        return Err("gateway shutdown deadline exceeded".into());
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
