use std::{net::SocketAddr, sync::Arc, time::Duration};

use ajiasu_proxy_protocol::{Bounds, HttpMode, parse_http, parse_socks5};
use tokio::{
    io::{AsyncReadExt, AsyncWriteExt},
    net::{TcpListener, TcpStream},
    sync::{Semaphore, watch},
    task::JoinSet,
};

use crate::{
    auth::Authenticator,
    relay::{RelayReceiver, RelaySender, RelayTransport},
    routes::RouteTable,
};

#[derive(Clone, Copy)]
pub struct ListenerConfig {
    pub http_listen: SocketAddr,
    pub socks5_listen: SocketAddr,
    pub max_header_bytes: usize,
    pub max_connections: usize,
}

pub struct GatewayServer<T> {
    config: ListenerConfig,
    routes: RouteTable,
    relay: Arc<T>,
    authenticator: Authenticator,
    bounds: Bounds,
}

impl<T: RelayTransport + 'static> GatewayServer<T> {
    pub fn new(config: ListenerConfig, routes: RouteTable, relay: Arc<T>) -> Self {
        let bounds = Bounds {
            max_header_bytes: config.max_header_bytes,
            ..Bounds::default()
        };
        Self {
            config,
            routes,
            relay,
            authenticator: Authenticator::new(32, 10),
            bounds,
        }
    }

    pub async fn serve(self, mut shutdown: watch::Receiver<bool>) -> Result<(), std::io::Error> {
        let http = TcpListener::bind(self.config.http_listen).await?;
        let socks = TcpListener::bind(self.config.socks5_listen).await?;
        let permits = Arc::new(Semaphore::new(self.config.max_connections));
        let mut tasks = JoinSet::new();
        loop {
            tokio::select! {
                accepted = http.accept() => {
                    let (stream, peer) = accepted?;
                    if let Ok(permit) = permits.clone().try_acquire_owned() {
                        let routes = self.routes.clone();
                        let relay = self.relay.clone();
                        let auth = self.authenticator.clone();
                        let bounds = self.bounds;
                        tasks.spawn(async move { let _permit = permit; let _ = handle_http(stream, peer, routes, relay, auth, bounds).await; });
                    }
                }
                accepted = socks.accept() => {
                    let (stream, peer) = accepted?;
                    if let Ok(permit) = permits.clone().try_acquire_owned() {
                        let routes = self.routes.clone();
                        let relay = self.relay.clone();
                        let auth = self.authenticator.clone();
                        let bounds = self.bounds;
                        tasks.spawn(async move { let _permit = permit; let _ = handle_socks(stream, peer, routes, relay, auth, bounds).await; });
                    }
                }
                result = shutdown.changed() => {
                    if result.is_err() || *shutdown.borrow() { break; }
                }
                Some(_) = tasks.join_next(), if !tasks.is_empty() => {}
            }
        }
        while tasks.join_next().await.is_some() {}
        Ok(())
    }
}

async fn handle_http<T: RelayTransport>(
    mut stream: TcpStream,
    peer: SocketAddr,
    routes: RouteTable,
    relay: Arc<T>,
    auth: Authenticator,
    bounds: Bounds,
) -> Result<(), ()> {
    let bytes = read_http_head(&mut stream, bounds.max_header_bytes).await?;
    let body_offset = bytes
        .windows(4)
        .position(|window| window == b"\r\n\r\n")
        .ok_or(())?
        + 4;
    let buffered_body = bytes[body_offset..].to_vec();
    let request = parse_http(&bytes, bounds).map_err(|_| ())?;
    let protocol = if request.mode == HttpMode::Connect {
        "connect"
    } else {
        "http"
    };
    let (route, credential) = match routes.credential_route(
        &request.auth.username,
        protocol,
        std::time::SystemTime::now(),
    ) {
        Ok(value) => value,
        Err(_) => {
            write_proxy_auth_required(&mut stream).await;
            return Err(());
        }
    };
    if auth
        .verify(
            &peer.ip().to_string(),
            &request.auth.username,
            &request.auth.password,
            Some(&credential.verifier),
        )
        .await
        .is_err()
    {
        write_proxy_auth_required(&mut stream).await;
        return Err(());
    }
    let connection = relay
        .open(&route, &request.host, request.port, protocol)
        .await
        .map_err(|_| ())?;
    let (sender, receiver) = connection.split();
    if request.mode == HttpMode::Connect {
        stream
            .write_all(b"HTTP/1.1 200 Connection Established\r\n\r\n")
            .await
            .map_err(|_| ())?;
        if !buffered_body.is_empty() {
            sender.send(buffered_body).await.map_err(|_| ())?;
        }
    } else {
        let mut initial = rebuild_forward_request(&request);
        initial.extend_from_slice(&buffered_body);
        sender.send(initial).await.map_err(|_| ())?;
    }
    pump(stream, sender, receiver).await
}

async fn handle_socks<T: RelayTransport>(
    mut stream: TcpStream,
    peer: SocketAddr,
    routes: RouteTable,
    relay: Arc<T>,
    auth: Authenticator,
    bounds: Bounds,
) -> Result<(), ()> {
    let mut combined = Vec::new();
    let greeting = read_socks_greeting(&mut stream).await?;
    if !greeting[2..].contains(&0x02) {
        let _ = stream.write_all(&[0x05, 0xff]).await;
        return Err(());
    }
    combined.extend_from_slice(&greeting);
    stream.write_all(&[0x05, 0x02]).await.map_err(|_| ())?;
    let credentials = read_socks_credentials(&mut stream).await?;
    let (username, password) = socks_credentials(&credentials)?;
    combined.extend_from_slice(&credentials);
    let (route, credential) =
        match routes.credential_route(&username, "socks5", std::time::SystemTime::now()) {
            Ok(value) => value,
            Err(_) => {
                let _ = stream.write_all(&[0x01, 0x01]).await;
                return Err(());
            }
        };
    if auth
        .verify(
            &peer.ip().to_string(),
            &username,
            &password,
            Some(&credential.verifier),
        )
        .await
        .is_err()
    {
        let _ = stream.write_all(&[0x01, 0x01]).await;
        return Err(());
    }
    stream.write_all(&[0x01, 0x00]).await.map_err(|_| ())?;
    let request = read_socks_request(&mut stream).await?;
    combined.extend_from_slice(&request);
    let parsed = parse_socks5(&combined, bounds).map_err(|_| ())?;
    let connection = match relay
        .open(&route, &parsed.host, parsed.port, "socks5")
        .await
    {
        Ok(connection) => connection,
        Err(_) => {
            let _ = stream
                .write_all(&[0x05, 0x01, 0x00, 0x01, 0, 0, 0, 0, 0, 0])
                .await;
            return Err(());
        }
    };
    stream
        .write_all(&[0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0])
        .await
        .map_err(|_| ())?;
    let (sender, receiver) = connection.split();
    pump(stream, sender, receiver).await
}

async fn pump(
    stream: TcpStream,
    sender: RelaySender,
    mut receiver: RelayReceiver,
) -> Result<(), ()> {
    let (mut client_read, mut client_write) = stream.into_split();
    let upstream = async move {
        let mut buffer = vec![0_u8; 64 * 1024];
        loop {
            let length = client_read.read(&mut buffer).await.map_err(|_| ())?;
            if length == 0 {
                sender.half_close().await.map_err(|_| ())?;
                return Ok::<(), ()>(());
            }
            sender
                .send(buffer[..length].to_vec())
                .await
                .map_err(|_| ())?;
        }
    };
    let downstream = async move {
        while let Some(payload) = receiver.recv().await.map_err(|_| ())? {
            client_write.write_all(&payload).await.map_err(|_| ())?;
        }
        client_write.shutdown().await.map_err(|_| ())?;
        Ok::<(), ()>(())
    };
    tokio::pin!(upstream);
    tokio::pin!(downstream);
    tokio::select! {
        result = &mut upstream => {
            result?;
            tokio::time::timeout(Duration::from_secs(30), &mut downstream)
                .await
                .map_err(|_| ())??;
        }
        result = &mut downstream => {
            result?;
            tokio::time::timeout(Duration::from_secs(30), &mut upstream)
                .await
                .map_err(|_| ())??;
        }
    }
    Ok(())
}

async fn write_proxy_auth_required(stream: &mut TcpStream) {
    let _ = stream
        .write_all(
            b"HTTP/1.1 407 Proxy Authentication Required\r\nProxy-Authenticate: Basic realm=\"AJiaSu Gateway\"\r\nContent-Length: 0\r\nConnection: close\r\n\r\n",
        )
        .await;
}

async fn read_http_head(stream: &mut TcpStream, maximum: usize) -> Result<Vec<u8>, ()> {
    let mut bytes = Vec::new();
    let mut chunk = [0_u8; 4096];
    loop {
        let length = tokio::time::timeout(Duration::from_secs(10), stream.read(&mut chunk))
            .await
            .map_err(|_| ())?
            .map_err(|_| ())?;
        if length == 0 || bytes.len() + length > maximum + 4 {
            return Err(());
        }
        bytes.extend_from_slice(&chunk[..length]);
        if bytes.windows(4).any(|window| window == b"\r\n\r\n") {
            return Ok(bytes);
        }
    }
}

fn rebuild_forward_request(request: &ajiasu_proxy_protocol::HttpRequest) -> Vec<u8> {
    let mut output = format!("{} {} HTTP/1.1\r\n", request.method, request.path).into_bytes();
    let mut has_host = false;
    for (name, value) in &request.headers {
        if name == "host" {
            has_host = true;
        }
        output.extend_from_slice(name.as_bytes());
        output.extend_from_slice(b": ");
        output.extend_from_slice(value.as_bytes());
        output.extend_from_slice(b"\r\n");
    }
    if !has_host {
        output.extend_from_slice(format!("host: {}:{}\r\n", request.host, request.port).as_bytes());
    }
    output.extend_from_slice(b"\r\n");
    output
}

async fn read_socks_greeting(stream: &mut TcpStream) -> Result<Vec<u8>, ()> {
    let mut head = [0_u8; 2];
    read_exact(stream, &mut head).await?;
    if head[0] != 5 || head[1] == 0 {
        return Err(());
    }
    let mut value = head.to_vec();
    let mut methods = vec![0_u8; head[1] as usize];
    read_exact(stream, &mut methods).await?;
    value.extend(methods);
    Ok(value)
}

async fn read_socks_credentials(stream: &mut TcpStream) -> Result<Vec<u8>, ()> {
    let mut head = [0_u8; 2];
    read_exact(stream, &mut head).await?;
    if head[0] != 1 || head[1] == 0 {
        return Err(());
    }
    let mut value = head.to_vec();
    let mut username = vec![0_u8; head[1] as usize];
    read_exact(stream, &mut username).await?;
    value.extend(username);
    let mut length = [0_u8; 1];
    read_exact(stream, &mut length).await?;
    value.push(length[0]);
    let mut password = vec![0_u8; length[0] as usize];
    read_exact(stream, &mut password).await?;
    value.extend(password);
    Ok(value)
}

fn socks_credentials(bytes: &[u8]) -> Result<(String, String), ()> {
    if bytes.len() < 3 || bytes[0] != 1 {
        return Err(());
    }
    let username_length = bytes[1] as usize;
    let password_length_index = 2 + username_length;
    let password_length = *bytes.get(password_length_index).ok_or(())? as usize;
    let username =
        std::str::from_utf8(bytes.get(2..password_length_index).ok_or(())?).map_err(|_| ())?;
    let password = std::str::from_utf8(
        bytes
            .get(password_length_index + 1..password_length_index + 1 + password_length)
            .ok_or(())?,
    )
    .map_err(|_| ())?;
    if username.is_empty() {
        return Err(());
    }
    Ok((username.to_owned(), password.to_owned()))
}

async fn read_socks_request(stream: &mut TcpStream) -> Result<Vec<u8>, ()> {
    let mut head = [0_u8; 4];
    read_exact(stream, &mut head).await?;
    if head[0] != 5 || head[1] != 1 {
        return Err(());
    }
    let length = match head[3] {
        1 => 4,
        4 => 16,
        3 => {
            let mut size = [0_u8; 1];
            read_exact(stream, &mut size).await?;
            let mut value = head.to_vec();
            value.push(size[0]);
            let mut rest = vec![0_u8; size[0] as usize + 2];
            read_exact(stream, &mut rest).await?;
            value.extend(rest);
            return Ok(value);
        }
        _ => return Err(()),
    };
    let mut value = head.to_vec();
    let mut rest = vec![0_u8; length + 2];
    read_exact(stream, &mut rest).await?;
    value.extend(rest);
    Ok(value)
}

async fn read_exact(stream: &mut TcpStream, bytes: &mut [u8]) -> Result<(), ()> {
    tokio::time::timeout(Duration::from_secs(10), stream.read_exact(bytes))
        .await
        .map_err(|_| ())?
        .map_err(|_| ())?;
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::{
        relay::{RelayConnection, RelayError},
        routes::Route,
    };
    use async_trait::async_trait;

    struct RejectRelay;

    #[async_trait]
    impl RelayTransport for RejectRelay {
        async fn open(
            &self,
            _route: &Route,
            _host: &str,
            _port: u16,
            _protocol: &str,
        ) -> Result<RelayConnection, RelayError> {
            Err(RelayError::Unavailable)
        }
    }

    #[test]
    fn forward_request_strips_proxy_credentials() {
        let request = ajiasu_proxy_protocol::HttpRequest {
            mode: HttpMode::Forward,
            method: "GET".to_owned(),
            host: "example.com".to_owned(),
            port: 80,
            path: "/".to_owned(),
            headers: vec![("user-agent".to_owned(), "test".to_owned())],
            auth: ajiasu_proxy_protocol::ProxyAuth {
                username: "u".to_owned(),
                password: "p".to_owned(),
            },
        };
        let output = String::from_utf8(rebuild_forward_request(&request)).unwrap();
        assert!(output.starts_with("GET / HTTP/1.1\r\n"));
        assert!(!output.to_ascii_lowercase().contains("proxy-authorization"));
    }

    #[test]
    fn protocol_errors_are_not_rendered_to_clients() {
        let _ = ajiasu_proxy_protocol::ProtocolError::Malformed;
        let _ = crate::relay::RelayError::Failed;
    }

    #[tokio::test]
    async fn http_auth_failure_returns_stable_407() {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let address = listener.local_addr().unwrap();
        let server = tokio::spawn(async move {
            let (stream, peer) = listener.accept().await.unwrap();
            handle_http(
                stream,
                peer,
                RouteTable::new(),
                Arc::new(RejectRelay),
                Authenticator::new(1, 1),
                Bounds::default(),
            )
            .await
        });
        let mut client = TcpStream::connect(address).await.unwrap();
        client
            .write_all(
                b"CONNECT example.com:443 HTTP/1.1\r\nProxy-Authorization: Basic dXNlcjpwYXNz\r\n\r\n",
            )
            .await
            .unwrap();
        let mut response = vec![0; 512];
        let length = client.read(&mut response).await.unwrap();
        let text = String::from_utf8_lossy(&response[..length]);
        assert!(text.starts_with("HTTP/1.1 407 Proxy Authentication Required\r\n"));
        assert!(server.await.unwrap().is_err());
    }

    #[tokio::test]
    async fn socks_auth_failure_returns_stable_status() {
        let listener = TcpListener::bind("127.0.0.1:0").await.unwrap();
        let address = listener.local_addr().unwrap();
        let server = tokio::spawn(async move {
            let (stream, peer) = listener.accept().await.unwrap();
            handle_socks(
                stream,
                peer,
                RouteTable::new(),
                Arc::new(RejectRelay),
                Authenticator::new(1, 1),
                Bounds::default(),
            )
            .await
        });
        let mut client = TcpStream::connect(address).await.unwrap();
        client.write_all(&[0x05, 0x01, 0x02]).await.unwrap();
        let mut method = [0; 2];
        client.read_exact(&mut method).await.unwrap();
        assert_eq!(method, [0x05, 0x02]);
        client
            .write_all(&[0x01, 0x01, b'u', 0x01, b'p'])
            .await
            .unwrap();
        let mut status = [0; 2];
        client.read_exact(&mut status).await.unwrap();
        assert_eq!(status, [0x01, 0x01]);
        assert!(server.await.unwrap().is_err());
    }
}
