use std::net::SocketAddr;

use ajiasu_gateway::config::GatewayConfig;

fn main() {
    let config = GatewayConfig {
        http_listen: SocketAddr::from(([0, 0, 0, 0], 8080)),
        socks5_listen: SocketAddr::from(([0, 0, 0, 0], 1080)),
        control_endpoint: std::env::var("AJIASU_CONTROL_ENDPOINT").unwrap_or_default(),
        max_header_bytes: 32 * 1024,
        max_connections: 1000,
    };
    if let Err(error) = config.validate() {
        eprintln!("gateway configuration invalid: {error}");
        std::process::exit(78);
    }
}
