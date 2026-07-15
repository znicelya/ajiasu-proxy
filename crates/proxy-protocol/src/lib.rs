#![forbid(unsafe_code)]

use base64::Engine;
use bytes::Bytes;
use thiserror::Error;

#[derive(Clone, Copy, Debug)]
pub struct Bounds {
    pub max_request_line: usize,
    pub max_header_count: usize,
    pub max_header_bytes: usize,
    pub max_target_bytes: usize,
    pub max_socks_domain_bytes: usize,
}

impl Default for Bounds {
    fn default() -> Self {
        Self {
            max_request_line: 8 * 1024,
            max_header_count: 64,
            max_header_bytes: 32 * 1024,
            max_target_bytes: 4096,
            max_socks_domain_bytes: 255,
        }
    }
}

#[derive(Debug, Error, Eq, PartialEq)]
pub enum ProtocolError {
    #[error("incomplete request")]
    Incomplete,
    #[error("request exceeds bounds")]
    TooLarge,
    #[error("malformed request")]
    Malformed,
    #[error("unsupported method")]
    UnsupportedMethod,
    #[error("proxy authentication required")]
    AuthenticationRequired,
    #[error("invalid proxy authentication")]
    InvalidAuthentication,
    #[error("unsupported socks method or command")]
    UnsupportedSocks,
    #[error("invalid target")]
    InvalidTarget,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum HttpMode {
    Forward,
    Connect,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ProxyAuth {
    pub username: String,
    pub password: String,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct HttpRequest {
    pub mode: HttpMode,
    pub method: String,
    pub host: String,
    pub port: u16,
    pub path: String,
    pub headers: Vec<(String, String)>,
    pub auth: ProxyAuth,
}

pub fn parse_http(input: &[u8], bounds: Bounds) -> Result<HttpRequest, ProtocolError> {
    let end = input
        .windows(4)
        .position(|window| window == b"\r\n\r\n")
        .ok_or(ProtocolError::Incomplete)?;
    if end > bounds.max_header_bytes {
        return Err(ProtocolError::TooLarge);
    }
    let text = std::str::from_utf8(&input[..end]).map_err(|_| ProtocolError::Malformed)?;
    let mut lines = text.split("\r\n");
    let request_line = lines.next().ok_or(ProtocolError::Malformed)?;
    if request_line.len() > bounds.max_request_line {
        return Err(ProtocolError::TooLarge);
    }
    let mut parts = request_line.split_whitespace();
    let method = parts
        .next()
        .ok_or(ProtocolError::Malformed)?
        .to_ascii_uppercase();
    let target = parts.next().ok_or(ProtocolError::Malformed)?;
    if parts.next() != Some("HTTP/1.1") || parts.next().is_some() {
        return Err(ProtocolError::Malformed);
    }
    let mut headers = Vec::new();
    let mut auth = None;
    for line in lines {
        let (name, value) = line.split_once(':').ok_or(ProtocolError::Malformed)?;
        let name = name.trim().to_ascii_lowercase();
        let value = value.trim().to_string();
        if name.is_empty() || name.chars().any(|character| character.is_control()) {
            return Err(ProtocolError::Malformed);
        }
        if name == "proxy-authorization" {
            auth = Some(parse_basic_auth(&value)?);
        }
        headers.push((name, value));
        if headers.len() > bounds.max_header_count {
            return Err(ProtocolError::TooLarge);
        }
    }
    let auth = auth.ok_or(ProtocolError::AuthenticationRequired)?;
    if target.len() > bounds.max_target_bytes {
        return Err(ProtocolError::TooLarge);
    }
    if method == "CONNECT" {
        let (host, port) = parse_authority(target)?;
        return Ok(HttpRequest {
            mode: HttpMode::Connect,
            method,
            host,
            port,
            path: String::new(),
            headers: strip_hop_by_hop(headers),
            auth,
        });
    }
    if method != "GET"
        && method != "HEAD"
        && method != "POST"
        && method != "PUT"
        && method != "PATCH"
        && method != "DELETE"
        && method != "OPTIONS"
    {
        return Err(ProtocolError::UnsupportedMethod);
    }
    let uri = url::Url::parse(target).map_err(|_| ProtocolError::InvalidTarget)?;
    if uri.scheme() != "http" || uri.host_str().is_none() {
        return Err(ProtocolError::InvalidTarget);
    }
    let host = uri
        .host_str()
        .ok_or(ProtocolError::InvalidTarget)?
        .to_string();
    let port = uri
        .port_or_known_default()
        .ok_or(ProtocolError::InvalidTarget)?;
    let path = match uri[url::Position::BeforePath..].is_empty() {
        true => "/".to_string(),
        false => uri[url::Position::BeforePath..].to_string(),
    };
    Ok(HttpRequest {
        mode: HttpMode::Forward,
        method,
        host,
        port,
        path,
        headers: strip_hop_by_hop(headers),
        auth,
    })
}

fn parse_basic_auth(value: &str) -> Result<ProxyAuth, ProtocolError> {
    let encoded = value
        .strip_prefix("Basic ")
        .or_else(|| value.strip_prefix("basic "))
        .ok_or(ProtocolError::InvalidAuthentication)?;
    let decoded = base64::engine::general_purpose::STANDARD
        .decode(encoded)
        .map_err(|_| ProtocolError::InvalidAuthentication)?;
    let value = String::from_utf8(decoded).map_err(|_| ProtocolError::InvalidAuthentication)?;
    let (username, password) = value
        .split_once(':')
        .ok_or(ProtocolError::InvalidAuthentication)?;
    if username.is_empty() || username.len() > 128 || password.len() > 1024 {
        return Err(ProtocolError::InvalidAuthentication);
    }
    Ok(ProxyAuth {
        username: username.to_string(),
        password: password.to_string(),
    })
}

fn parse_authority(value: &str) -> Result<(String, u16), ProtocolError> {
    let (host, port) = if let Some(rest) = value.strip_prefix('[') {
        let end = rest.find(']').ok_or(ProtocolError::InvalidTarget)?;
        let host = &rest[..end];
        let port = rest
            .get(end + 1..)
            .and_then(|value| value.strip_prefix(':'))
            .ok_or(ProtocolError::InvalidTarget)?;
        (host, port)
    } else {
        value.rsplit_once(':').ok_or(ProtocolError::InvalidTarget)?
    };
    let port = port
        .parse::<u16>()
        .map_err(|_| ProtocolError::InvalidTarget)?;
    if port == 0 || host.is_empty() {
        return Err(ProtocolError::InvalidTarget);
    };
    Ok((host.to_string(), port))
}

fn strip_hop_by_hop(headers: Vec<(String, String)>) -> Vec<(String, String)> {
    headers
        .into_iter()
        .filter(|(name, _)| {
            !matches!(
                name.as_str(),
                "proxy-authorization"
                    | "proxy-connection"
                    | "connection"
                    | "keep-alive"
                    | "te"
                    | "trailer"
                    | "transfer-encoding"
                    | "upgrade"
            )
        })
        .collect()
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct SocksRequest {
    pub auth: ProxyAuth,
    pub host: String,
    pub port: u16,
}

pub fn parse_socks5(input: &[u8], bounds: Bounds) -> Result<SocksRequest, ProtocolError> {
    let mut cursor = Cursor::new(input);
    if cursor.read_u8()? != 0x05 {
        return Err(ProtocolError::Malformed);
    }
    let method_count = cursor.read_u8()? as usize;
    let methods = cursor.read_bytes(method_count)?;
    if !methods.contains(&0x02) {
        return Err(ProtocolError::UnsupportedSocks);
    }
    if cursor.read_u8()? != 0x01 {
        return Err(ProtocolError::Malformed);
    }
    let username_length = cursor.read_u8()? as usize;
    let username = cursor.read_bytes(username_length)?;
    let password_length = cursor.read_u8()? as usize;
    let password = cursor.read_bytes(password_length)?;
    let username = std::str::from_utf8(username)
        .map_err(|_| ProtocolError::InvalidAuthentication)?
        .to_string();
    let password = std::str::from_utf8(password)
        .map_err(|_| ProtocolError::InvalidAuthentication)?
        .to_string();
    if username.is_empty() || username.len() > 128 || password.len() > 1024 {
        return Err(ProtocolError::InvalidAuthentication);
    }
    if cursor.read_u8()? != 0x05 || cursor.read_u8()? != 0x01 {
        return Err(ProtocolError::UnsupportedSocks);
    }
    if cursor.read_u8()? != 0 {
        return Err(ProtocolError::Malformed);
    }
    let host = match cursor.read_u8()? {
        0x01 => {
            let bytes = cursor.read_bytes(4)?;
            std::net::Ipv4Addr::new(bytes[0], bytes[1], bytes[2], bytes[3]).to_string()
        }
        0x03 => {
            let length = cursor.read_u8()? as usize;
            if length == 0 || length > bounds.max_socks_domain_bytes {
                return Err(ProtocolError::TooLarge);
            };
            std::str::from_utf8(cursor.read_bytes(length)?)
                .map_err(|_| ProtocolError::InvalidTarget)?
                .to_string()
        }
        0x04 => {
            let bytes = cursor.read_bytes(16)?;
            let mut array = [0u8; 16];
            array.copy_from_slice(bytes);
            std::net::Ipv6Addr::from(array).to_string()
        }
        _ => return Err(ProtocolError::InvalidTarget),
    };
    let port = u16::from_be_bytes([cursor.read_u8()?, cursor.read_u8()?]);
    if port == 0 {
        return Err(ProtocolError::InvalidTarget);
    }
    Ok(SocksRequest {
        auth: ProxyAuth { username, password },
        host,
        port,
    })
}

struct Cursor<'a> {
    bytes: &'a [u8],
    offset: usize,
}
impl<'a> Cursor<'a> {
    fn new(bytes: &'a [u8]) -> Self {
        Self { bytes, offset: 0 }
    }
    fn read_u8(&mut self) -> Result<u8, ProtocolError> {
        let value = *self
            .bytes
            .get(self.offset)
            .ok_or(ProtocolError::Incomplete)?;
        self.offset += 1;
        Ok(value)
    }
    fn read_bytes(&mut self, length: usize) -> Result<&'a [u8], ProtocolError> {
        if length > 4096 {
            return Err(ProtocolError::TooLarge);
        };
        let end = self
            .offset
            .checked_add(length)
            .ok_or(ProtocolError::TooLarge)?;
        let value = self
            .bytes
            .get(self.offset..end)
            .ok_or(ProtocolError::Incomplete)?;
        self.offset = end;
        Ok(value)
    }
}

pub fn as_bytes(input: &[u8]) -> Bytes {
    Bytes::copy_from_slice(input)
}

#[cfg(test)]
mod tests {
    use super::*;
    use base64::Engine;

    #[test]
    fn parses_forward_and_strips_proxy_headers() {
        let auth = base64::engine::general_purpose::STANDARD.encode("user:pass");
        let request = format!(
            "GET http://example.com/path?q=1 HTTP/1.1\r\nProxy-Authorization: Basic {auth}\r\nConnection: keep-alive\r\nX-Test: ok\r\n\r\n"
        );
        let parsed = parse_http(request.as_bytes(), Bounds::default()).unwrap();
        assert_eq!(parsed.host, "example.com");
        assert_eq!(parsed.path, "/path?q=1");
        assert_eq!(parsed.headers, vec![("x-test".into(), "ok".into())]);
    }

    #[test]
    fn rejects_unsupported_http_and_socks_methods() {
        assert_eq!(
            parse_http(
                b"TRACE http://example.com/ HTTP/1.1\r\nProxy-Authorization: Basic dTpw\r\n\r\n",
                Bounds::default()
            ),
            Err(ProtocolError::UnsupportedMethod)
        );
        assert_eq!(
            parse_socks5(&[5, 1, 0], Bounds::default()),
            Err(ProtocolError::UnsupportedSocks)
        );
    }

    #[test]
    fn parses_socks_domain_connect_with_auth() {
        let bytes = [
            5, 1, 2, 1, 4, b'u', b's', b'e', b'r', 4, b'p', b'a', b's', b's', 5, 1, 0, 3, 11, b'e',
            b'x', b'a', b'm', b'p', b'l', b'e', b'.', b'c', b'o', b'm', 0, 80,
        ];
        let request = parse_socks5(&bytes, Bounds::default()).unwrap();
        assert_eq!(request.host, "example.com");
        assert_eq!(request.port, 80);
    }

    #[test]
    fn bounds_oversized_and_incomplete_inputs() {
        let bounds = Bounds {
            max_header_bytes: 8,
            ..Bounds::default()
        };
        assert_eq!(
            parse_http(b"GET http://example.com/ HTTP/1.1\r\n\r\n", bounds),
            Err(ProtocolError::TooLarge)
        );
        assert_eq!(
            parse_http(b"GET http://example.com/ HTTP/1.1\r\n", Bounds::default()),
            Err(ProtocolError::Incomplete)
        );
        let mut socks = vec![5, 1, 2, 1, 1, b'u', 1, b'p', 5, 1, 0, 3, 255];
        socks.extend(std::iter::repeat_n(b'a', 10));
        assert_eq!(
            parse_socks5(&socks, Bounds::default()),
            Err(ProtocolError::Incomplete)
        );
    }
}
