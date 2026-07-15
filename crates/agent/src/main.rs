#![forbid(unsafe_code)]

use ajiasu_agent::{client, config::Config};
use tracing::info;

#[tokio::main]
async fn main() -> Result<(), Box<dyn std::error::Error>> {
    tracing_subscriber::fmt()
        .json()
        .with_env_filter("info")
        .init();
    let config = Config::from_env()?;
    info!(
        event = "agent_starting",
        node_name = %config.node_name,
        runtime = %config.runtime,
    );
    client::run(config).await?;
    Ok(())
}
