use ed25519_dalek::{Signature, Verifier, VerifyingKey};
use thiserror::Error;
use time::{OffsetDateTime, format_description::well_known::Rfc3339};
use uuid::Uuid;

#[derive(Clone, Debug)]
pub struct RouteGrant {
    pub gateway_id: Uuid,
    pub tenant_id: Uuid,
    pub endpoint_id: Uuid,
    pub runner_id: Uuid,
    pub generation: u64,
    pub protocols: Vec<String>,
    pub policy_hash: String,
    pub expires_at: OffsetDateTime,
    pub signature: Vec<u8>,
}

#[derive(Debug, Error, Eq, PartialEq)]
pub enum GrantError {
    #[error("invalid route grant")]
    Invalid,
}

pub struct GrantExpectation<'a> {
    pub gateway_id: Uuid,
    pub runner_id: Uuid,
    pub generation: u64,
    pub protocol: &'a str,
    pub policy_hash: &'a str,
    pub now: OffsetDateTime,
}

impl RouteGrant {
    pub(crate) fn signing_bytes(&self) -> Result<Vec<u8>, GrantError> {
        let mut protocols = self.protocols.clone();
        protocols.sort();
        let expires = self
            .expires_at
            .format(&Rfc3339)
            .map_err(|_| GrantError::Invalid)?;
        Ok(format!("gateway={}\ntenant={}\nendpoint={}\nrunner={}\ngeneration={}\nprotocols={}\npolicy_hash={}\nexpires_at={}\n", self.gateway_id, self.tenant_id, self.endpoint_id, self.runner_id, self.generation, protocols.join(","), self.policy_hash, expires).into_bytes())
    }

    pub fn verify(
        &self,
        key: &VerifyingKey,
        expected: GrantExpectation<'_>,
    ) -> Result<(), GrantError> {
        if self.gateway_id != expected.gateway_id
            || self.runner_id != expected.runner_id
            || self.generation != expected.generation
            || self.policy_hash != expected.policy_hash
            || self.expires_at <= expected.now
            || self.gateway_id.is_nil()
            || self.tenant_id.is_nil()
            || self.endpoint_id.is_nil()
            || self.runner_id.is_nil()
            || !self
                .protocols
                .iter()
                .any(|value| value == expected.protocol)
        {
            return Err(GrantError::Invalid);
        }
        let signature = Signature::from_slice(&self.signature).map_err(|_| GrantError::Invalid)?;
        key.verify(&self.signing_bytes()?, &signature)
            .map_err(|_| GrantError::Invalid)
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    use ed25519_dalek::{Signer, SigningKey};

    #[test]
    fn verifies_audience_generation_protocol_and_expiry() {
        let signing = SigningKey::from_bytes(&[7u8; 32]);
        let now = OffsetDateTime::UNIX_EPOCH;
        let mut grant = RouteGrant {
            gateway_id: Uuid::from_u128(1),
            tenant_id: Uuid::from_u128(2),
            endpoint_id: Uuid::from_u128(3),
            runner_id: Uuid::from_u128(4),
            generation: 9,
            protocols: vec!["connect".into()],
            policy_hash: "hash".into(),
            expires_at: now + time::Duration::minutes(1),
            signature: Vec::new(),
        };
        grant.signature = signing
            .sign(&grant.signing_bytes().unwrap())
            .to_bytes()
            .to_vec();
        grant
            .verify(
                &signing.verifying_key(),
                GrantExpectation {
                    gateway_id: grant.gateway_id,
                    runner_id: grant.runner_id,
                    generation: 9,
                    protocol: "connect",
                    policy_hash: "hash",
                    now,
                },
            )
            .unwrap();
        assert_eq!(
            grant.verify(
                &signing.verifying_key(),
                GrantExpectation {
                    gateway_id: grant.gateway_id,
                    runner_id: grant.runner_id,
                    generation: 8,
                    protocol: "connect",
                    policy_hash: "hash",
                    now
                }
            ),
            Err(GrantError::Invalid)
        );
    }
}
