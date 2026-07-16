#![forbid(unsafe_code)]

use std::net::IpAddr;

use ajiasu_proxy_policy::dns;
use ipnet::IpNet;
use thiserror::Error;

pub const MAX_DATA_FRAME: usize = 64 * 1024;
pub const MAX_METADATA_FRAME: usize = 4 + 1 + 1 + 2 + 2 + 4096;
const METADATA_MAGIC: &[u8; 4] = b"AJR1";

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Metadata {
    pub protocol: String,
    pub target_host: String,
    pub target_port: u16,
    pub dns_mode: String,
}

pub fn encode_metadata(metadata: &Metadata) -> Result<Vec<u8>, RelayError> {
    let protocol = match metadata.protocol.as_str() {
        "http" => 1,
        "connect" => 2,
        "socks5" => 3,
        _ => return Err(RelayError::InvalidMetadata),
    };
    let dns_mode = match metadata.dns_mode.as_str() {
        "gateway" => 1,
        "runner" => 2,
        _ => return Err(RelayError::InvalidMetadata),
    };
    let host = metadata.target_host.as_bytes();
    let host_length = u16::try_from(host.len()).map_err(|_| RelayError::InvalidMetadata)?;
    if host.is_empty() || host.len() > 4096 || metadata.target_port == 0 {
        return Err(RelayError::InvalidMetadata);
    }
    let mut frame = Vec::with_capacity(10 + host.len());
    frame.extend_from_slice(METADATA_MAGIC);
    frame.push(protocol);
    frame.push(dns_mode);
    frame.extend_from_slice(&host_length.to_be_bytes());
    frame.extend_from_slice(&metadata.target_port.to_be_bytes());
    frame.extend_from_slice(host);
    Ok(frame)
}

pub fn decode_metadata(frame: &[u8]) -> Result<Metadata, RelayError> {
    if frame.len() < 10 || frame.len() > MAX_METADATA_FRAME || &frame[..4] != METADATA_MAGIC {
        return Err(RelayError::InvalidMetadata);
    }
    let protocol = match frame[4] {
        1 => "http",
        2 => "connect",
        3 => "socks5",
        _ => return Err(RelayError::InvalidMetadata),
    };
    let dns_mode = match frame[5] {
        1 => "gateway",
        2 => "runner",
        _ => return Err(RelayError::InvalidMetadata),
    };
    let host_length = usize::from(u16::from_be_bytes([frame[6], frame[7]]));
    if host_length == 0 || frame.len() != 10 + host_length {
        return Err(RelayError::InvalidMetadata);
    }
    let target_port = u16::from_be_bytes([frame[8], frame[9]]);
    let target_host = std::str::from_utf8(&frame[10..])
        .map_err(|_| RelayError::InvalidMetadata)?
        .to_owned();
    let metadata = Metadata {
        protocol: protocol.to_owned(),
        target_host,
        target_port,
        dns_mode: dns_mode.to_owned(),
    };
    let mut session = RelaySession::default();
    session.open(metadata.clone())?;
    Ok(metadata)
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
enum State {
    WaitingMetadata,
    Streaming,
    HalfClosed,
    Closed,
}

#[derive(Debug, Error, Eq, PartialEq)]
pub enum RelayError {
    #[error("metadata is invalid or duplicated")]
    InvalidMetadata,
    #[error("data frame is too large")]
    FrameTooLarge,
    #[error("relay state is invalid")]
    InvalidState,
    #[error("dns answer is denied")]
    DnsDenied,
}

pub struct RelaySession {
    state: State,
    metadata: Option<Metadata>,
    bytes: u64,
}
impl Default for RelaySession {
    fn default() -> Self {
        Self {
            state: State::WaitingMetadata,
            metadata: None,
            bytes: 0,
        }
    }
}
impl RelaySession {
    pub fn open(&mut self, metadata: Metadata) -> Result<(), RelayError> {
        if self.state != State::WaitingMetadata
            || metadata.target_host.is_empty()
            || metadata.target_host.len() > 4096
            || metadata.target_port == 0
            || !matches!(metadata.protocol.as_str(), "http" | "connect" | "socks5")
            || !matches!(metadata.dns_mode.as_str(), "gateway" | "runner")
        {
            return Err(RelayError::InvalidMetadata);
        };
        self.metadata = Some(metadata);
        self.state = State::Streaming;
        Ok(())
    }
    pub fn data(&mut self, frame: &[u8]) -> Result<(), RelayError> {
        if self.state != State::Streaming {
            return Err(RelayError::InvalidState);
        };
        if frame.len() > MAX_DATA_FRAME {
            return Err(RelayError::FrameTooLarge);
        };
        self.bytes = self.bytes.saturating_add(frame.len() as u64);
        Ok(())
    }
    pub fn half_close(&mut self) -> Result<(), RelayError> {
        if self.state != State::Streaming {
            return Err(RelayError::InvalidState);
        };
        self.state = State::HalfClosed;
        Ok(())
    }
    pub fn close(&mut self) {
        self.state = State::Closed;
    }
    pub fn bytes(&self) -> u64 {
        self.bytes
    }
}

pub fn validate_runner_dns(answers: &[IpAddr], management: &[IpNet]) -> Result<(), RelayError> {
    dns::validate_answers(answers, management).map_err(|_| RelayError::DnsDenied)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn metadata_once_frames_and_half_close_are_bounded() {
        let mut session = RelaySession::default();
        let metadata = Metadata {
            protocol: "connect".into(),
            target_host: "example.com".into(),
            target_port: 443,
            dns_mode: "runner".into(),
        };
        session.open(metadata.clone()).unwrap();
        assert_eq!(session.open(metadata), Err(RelayError::InvalidMetadata));
        session.data(&vec![0; MAX_DATA_FRAME]).unwrap();
        assert_eq!(
            session.data(&vec![0; MAX_DATA_FRAME + 1]),
            Err(RelayError::FrameTooLarge)
        );
        session.half_close().unwrap();
        assert_eq!(session.data(b"late"), Err(RelayError::InvalidState));
    }

    #[test]
    fn runner_dns_rechecks_every_answer() {
        let answers = [
            "8.8.8.8".parse().unwrap(),
            "169.254.169.254".parse().unwrap(),
        ];
        assert_eq!(
            validate_runner_dns(&answers, &[]),
            Err(RelayError::DnsDenied)
        );
    }

    #[test]
    fn metadata_wire_frame_round_trips_and_rejects_truncation() {
        let metadata = Metadata {
            protocol: "connect".into(),
            target_host: "example.com".into(),
            target_port: 443,
            dns_mode: "runner".into(),
        };
        let encoded = encode_metadata(&metadata).unwrap();
        assert_eq!(decode_metadata(&encoded), Ok(metadata));
        assert_eq!(
            decode_metadata(&encoded[..encoded.len() - 1]),
            Err(RelayError::InvalidMetadata)
        );
    }
}
