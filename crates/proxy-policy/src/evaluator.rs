use std::{collections::BTreeSet, net::IpAddr, str::FromStr};

use ipnet::IpNet;
use serde::{Deserialize, Serialize};
use sha2::{Digest, Sha256};
use thiserror::Error;

use crate::{
    cidr, dns, domain,
    limits::Limits,
    port::{self, PortRange},
};

#[derive(Clone, Copy, Debug, Eq, PartialEq, Ord, PartialOrd, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum Protocol {
    Http,
    Connect,
    Socks5,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(rename_all = "lowercase")]
pub enum DnsMode {
    Gateway,
    Runner,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(default, deny_unknown_fields)]
pub struct Policy {
    pub protocols: Vec<Protocol>,
    pub dns_mode: DnsMode,
    pub source_cidrs: Vec<IpNet>,
    pub target_allow_cidrs: Vec<IpNet>,
    pub target_deny_cidrs: Vec<IpNet>,
    pub target_allow_domains: Vec<String>,
    pub target_deny_domains: Vec<String>,
    pub allowed_ports: Vec<PortRange>,
    pub limits: Limits,
}

impl Default for Policy {
    fn default() -> Self {
        Self {
            protocols: vec![Protocol::Http],
            dns_mode: DnsMode::Gateway,
            source_cidrs: Vec::new(),
            target_allow_cidrs: Vec::new(),
            target_deny_cidrs: Vec::new(),
            target_allow_domains: Vec::new(),
            target_deny_domains: Vec::new(),
            allowed_ports: Vec::new(),
            limits: Limits::default(),
        }
    }
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct CompiledPolicy {
    pub policy: Policy,
    pub canonical_json: Vec<u8>,
    pub hash: String,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Target {
    pub host: String,
    pub port: u16,
    pub resolved: Vec<IpAddr>,
}

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum Decision {
    Allow,
    Deny,
}

#[derive(Debug, Error, Eq, PartialEq)]
pub enum PolicyError {
    #[error("invalid policy json")]
    InvalidJson,
    #[error("protocol set must be non-empty")]
    EmptyProtocols,
    #[error("invalid domain")]
    InvalidDomain,
    #[error("contradictory domain rule")]
    ContradictoryDomain,
    #[error("contradictory cidr rule")]
    ContradictoryCidr,
    #[error("invalid port range")]
    InvalidPortRange,
    #[error("invalid resource limit")]
    InvalidLimit,
    #[error("invalid traffic window")]
    InvalidTrafficWindow,
    #[error("invalid target")]
    InvalidTarget,
    #[error("source denied")]
    SourceDenied,
    #[error("protocol denied")]
    ProtocolDenied,
    #[error("port denied")]
    PortDenied,
    #[error("platform safety deny")]
    PlatformDenied,
    #[error("explicit policy deny")]
    ExplicitDenied,
    #[error("target is outside allowlist")]
    NotAllowed,
    #[error("dns returned no addresses")]
    NoDnsAnswers,
}

impl Policy {
    pub fn compile_json(raw: &[u8]) -> Result<CompiledPolicy, PolicyError> {
        let policy: Policy = serde_json::from_slice(raw).map_err(|_| PolicyError::InvalidJson)?;
        policy.compile()
    }

    pub fn compile(mut self) -> Result<CompiledPolicy, PolicyError> {
        if self.protocols.is_empty() {
            return Err(PolicyError::EmptyProtocols);
        }
        self.protocols = self
            .protocols
            .into_iter()
            .collect::<BTreeSet<_>>()
            .into_iter()
            .collect();
        normalize_nets(&mut self.source_cidrs);
        normalize_nets(&mut self.target_allow_cidrs);
        normalize_nets(&mut self.target_deny_cidrs);
        self.target_allow_domains = normalize_domains(self.target_allow_domains)?;
        self.target_deny_domains = normalize_domains(self.target_deny_domains)?;
        self.allowed_ports = port::normalize(self.allowed_ports)?;
        self.limits.validate()?;
        if self
            .target_allow_domains
            .iter()
            .any(|allow| self.target_deny_domains.iter().any(|deny| allow == deny))
        {
            return Err(PolicyError::ContradictoryDomain);
        }
        if self
            .target_allow_cidrs
            .iter()
            .any(|allow| self.target_deny_cidrs.iter().any(|deny| allow == deny))
        {
            return Err(PolicyError::ContradictoryCidr);
        }
        let canonical_json = serde_json::to_vec(&self).map_err(|_| PolicyError::InvalidJson)?;
        let hash = hex::encode(Sha256::digest(&canonical_json));
        Ok(CompiledPolicy {
            policy: self,
            canonical_json,
            hash,
        })
    }
}

impl CompiledPolicy {
    pub fn evaluate(
        &self,
        protocol: Protocol,
        source: IpAddr,
        target: &Target,
        management: &[IpNet],
    ) -> Result<Decision, PolicyError> {
        if !self.policy.protocols.contains(&protocol) {
            return Err(PolicyError::ProtocolDenied);
        }
        if !self.policy.source_cidrs.is_empty()
            && !self
                .policy
                .source_cidrs
                .iter()
                .any(|network| network.contains(&source))
        {
            return Err(PolicyError::SourceDenied);
        }
        if target.port == 0
            || (!self.policy.allowed_ports.is_empty()
                && !self
                    .policy
                    .allowed_ports
                    .iter()
                    .any(|range| range.contains(target.port)))
        {
            return Err(PolicyError::PortDenied);
        }
        let numeric = IpAddr::from_str(&target.host).ok();
        let host = if numeric.is_none() {
            Some(domain::canonicalize(&target.host)?)
        } else {
            None
        };
        let mut addresses = target.resolved.clone();
        if let Some(address) = numeric {
            addresses.push(address);
        }
        if !addresses.is_empty() {
            dns::validate_answers(&addresses, management)?;
        }
        if addresses.iter().any(|address| {
            self.policy
                .target_deny_cidrs
                .iter()
                .any(|network| network.contains(address))
        }) || host.as_ref().is_some_and(|host| {
            self.policy
                .target_deny_domains
                .iter()
                .any(|rule| domain::matches(rule, host))
        }) {
            return Err(PolicyError::ExplicitDenied);
        }
        let has_allowlist = !self.policy.target_allow_cidrs.is_empty()
            || !self.policy.target_allow_domains.is_empty();
        let allowed_ip = addresses.iter().all(|address| {
            self.policy
                .target_allow_cidrs
                .iter()
                .any(|network| network.contains(address))
        });
        let allowed_host = host.as_ref().is_some_and(|host| {
            self.policy
                .target_allow_domains
                .iter()
                .any(|rule| domain::matches(rule, host))
        });
        if has_allowlist && !(allowed_host || (!addresses.is_empty() && allowed_ip)) {
            return Err(PolicyError::NotAllowed);
        }
        if addresses
            .iter()
            .any(|address| cidr::platform_denied(*address, management))
        {
            return Err(PolicyError::PlatformDenied);
        }
        Ok(Decision::Allow)
    }
}

fn normalize_nets(values: &mut Vec<IpNet>) {
    values.sort_by_key(ToString::to_string);
    values.dedup();
}

fn normalize_domains(values: Vec<String>) -> Result<Vec<String>, PolicyError> {
    let mut values = values
        .into_iter()
        .map(|value| domain::canonicalize(&value))
        .collect::<Result<Vec<_>, _>>()?;
    values.sort();
    values.dedup();
    Ok(values)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[derive(Deserialize)]
    struct GoldenVector {
        name: String,
        input: serde_json::Value,
        canonical: String,
        sha256: String,
    }

    #[test]
    fn matches_shared_go_rust_golden_vectors() {
        let vectors: Vec<GoldenVector> = serde_json::from_str(include_str!(
            "../../../tests/fixtures/phase5/policy_vectors.json"
        ))
        .unwrap();
        for vector in vectors {
            let compiled =
                Policy::compile_json(serde_json::to_string(&vector.input).unwrap().as_bytes())
                    .unwrap();
            assert_eq!(
                compiled.canonical_json,
                vector.canonical.as_bytes(),
                "{}",
                vector.name
            );
            assert_eq!(compiled.hash, vector.sha256, "{}", vector.name);
        }
    }

    #[test]
    fn canonicalizes_and_blocks_every_unsafe_dns_answer() {
        let raw = r#"{"protocols":["socks5","http","http"],"target_allow_domains":["BÜCHER.Example."],"allowed_ports":[{"from":443,"to":443},{"from":80,"to":80}],"limits":{"max_connections":10,"max_connection_rate":5,"idle_timeout_seconds":30,"max_bytes_per_connection":1000,"traffic_window_seconds":60,"max_window_bytes":10000}}"#;
        let compiled = Policy::compile_json(raw.as_bytes()).unwrap();
        assert!(
            String::from_utf8_lossy(&compiled.canonical_json).contains("xn--bcher-kva.example")
        );
        let target = Target {
            host: "bücher.example".into(),
            port: 443,
            resolved: vec!["8.8.8.8".parse().unwrap(), "127.0.0.1".parse().unwrap()],
        };
        assert_eq!(
            compiled.evaluate(
                Protocol::Http,
                "203.0.113.10".parse().unwrap(),
                &target,
                &[]
            ),
            Err(PolicyError::PlatformDenied)
        );
    }

    #[test]
    fn rejects_wildcards_and_contradictions() {
        assert_eq!(
            Policy::compile_json(br#"{"target_allow_domains":["*.example.com"]}"#),
            Err(PolicyError::InvalidDomain)
        );
        assert_eq!(
            Policy::compile_json(
                br#"{"target_allow_cidrs":["8.8.8.0/24"],"target_deny_cidrs":["8.8.8.0/24"]}"#
            ),
            Err(PolicyError::ContradictoryCidr)
        );
    }
}
