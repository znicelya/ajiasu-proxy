#![forbid(unsafe_code)]

use std::net::IpAddr;

use ajiasu_proxy_policy::dns;
use ipnet::IpNet;
use thiserror::Error;

pub const MAX_DATA_FRAME: usize = 64 * 1024;

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Metadata {
    pub protocol: String,
    pub target_host: String,
    pub target_port: u16,
    pub dns_mode: String,
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
}
