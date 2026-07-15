use std::net::IpAddr;

use ipnet::IpNet;

use crate::{PolicyError, cidr};

pub fn validate_answers(answers: &[IpAddr], management: &[IpNet]) -> Result<(), PolicyError> {
    if answers.is_empty() {
        return Err(PolicyError::NoDnsAnswers);
    }
    if answers
        .iter()
        .any(|address| cidr::platform_denied(*address, management))
    {
        return Err(PolicyError::PlatformDenied);
    }
    Ok(())
}
