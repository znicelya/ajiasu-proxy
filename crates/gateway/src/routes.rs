use std::{
    collections::HashMap,
    sync::{Arc, RwLock},
    time::SystemTime,
};

use thiserror::Error;
use uuid::Uuid;

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Grant {
    pub gateway_id: Uuid,
    pub runner_id: Uuid,
    pub generation: u64,
    pub policy_hash: String,
    pub expires_at: SystemTime,
    pub protocols: Vec<String>,
    pub signature: Vec<u8>,
}
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Credential {
    pub id: Uuid,
    pub public_identifier: String,
    pub verifier: String,
    pub expires_at: Option<SystemTime>,
    pub revoked: bool,
}
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Route {
    pub tenant_id: Uuid,
    pub endpoint_id: Uuid,
    pub policy_hash: String,
    pub protocols: Vec<String>,
    pub runner_id: Uuid,
    pub generation: u64,
    pub grant: Grant,
    pub credentials: Vec<Credential>,
}
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Snapshot {
    pub version: u64,
    pub routes: Vec<Route>,
}
#[derive(Clone, Debug, Eq, PartialEq)]
pub struct Delta {
    pub version: u64,
    pub route: Route,
    pub revoked: bool,
}

#[derive(Debug, Error, Eq, PartialEq)]
pub enum RouteError {
    #[error("route snapshot version is stale")]
    StaleVersion,
    #[error("route not found")]
    NotFound,
    #[error("route grant is expired or mismatched")]
    StaleGrant,
    #[error("protocol is not enabled")]
    ProtocolDenied,
}

#[derive(Clone)]
pub struct RouteTable {
    inner: Arc<RwLock<State>>,
}
#[derive(Default)]
struct State {
    version: u64,
    routes: HashMap<(Uuid, Uuid), Route>,
}
impl RouteTable {
    pub fn new() -> Self {
        Self {
            inner: Arc::new(RwLock::new(State::default())),
        }
    }
    pub fn apply_snapshot(&self, snapshot: Snapshot) -> Result<(), RouteError> {
        let mut state = self.inner.write().map_err(|_| RouteError::StaleVersion)?;
        if snapshot.version == 0 || snapshot.version < state.version {
            return Err(RouteError::StaleVersion);
        }
        let routes = snapshot
            .routes
            .into_iter()
            .map(|route| ((route.tenant_id, route.endpoint_id), route))
            .collect();
        state.routes = routes;
        state.version = snapshot.version;
        Ok(())
    }
    pub fn apply_delta(&self, delta: Delta) -> Result<(), RouteError> {
        let mut state = self.inner.write().map_err(|_| RouteError::StaleVersion)?;
        if delta.version <= state.version {
            return Err(RouteError::StaleVersion);
        }
        let key = (delta.route.tenant_id, delta.route.endpoint_id);
        if delta.revoked {
            state.routes.remove(&key);
        } else {
            state.routes.insert(key, delta.route);
        }
        state.version = delta.version;
        Ok(())
    }
    pub fn select(
        &self,
        tenant_id: Uuid,
        endpoint_id: Uuid,
        protocol: &str,
        now: SystemTime,
    ) -> Result<Route, RouteError> {
        let state = self.inner.read().map_err(|_| RouteError::NotFound)?;
        let route = state
            .routes
            .get(&(tenant_id, endpoint_id))
            .cloned()
            .ok_or(RouteError::NotFound)?;
        if !route.protocols.iter().any(|item| item == protocol)
            || !route.grant.protocols.iter().any(|item| item == protocol)
        {
            return Err(RouteError::ProtocolDenied);
        }
        if route.grant.gateway_id.is_nil()
            || route.grant.runner_id != route.runner_id
            || route.grant.generation != route.generation
            || route.grant.policy_hash != route.policy_hash
            || route.grant.expires_at <= now
        {
            return Err(RouteError::StaleGrant);
        }
        Ok(route)
    }
    pub fn version(&self) -> u64 {
        self.inner
            .read()
            .map(|state| state.version)
            .unwrap_or_default()
    }
}

impl Default for RouteTable {
    fn default() -> Self {
        Self::new()
    }
}

#[cfg(test)]
mod tests {
    use super::*;
    fn route() -> Route {
        let gateway = Uuid::from_u128(1);
        let tenant = Uuid::from_u128(2);
        let endpoint = Uuid::from_u128(3);
        let runner = Uuid::from_u128(4);
        let expiry = SystemTime::now() + std::time::Duration::from_secs(60);
        Route {
            tenant_id: tenant,
            endpoint_id: endpoint,
            policy_hash: "hash".into(),
            protocols: vec!["connect".into()],
            runner_id: runner,
            generation: 2,
            grant: Grant {
                gateway_id: gateway,
                runner_id: runner,
                generation: 2,
                policy_hash: "hash".into(),
                expires_at: expiry,
                protocols: vec!["connect".into()],
                signature: vec![1],
            },
            credentials: vec![],
        }
    }
    #[test]
    fn applies_atomically_and_rejects_reordering() {
        let table = RouteTable::new();
        let item = route();
        table
            .apply_snapshot(Snapshot {
                version: 3,
                routes: vec![item.clone()],
            })
            .unwrap();
        assert_eq!(
            table.apply_delta(Delta {
                version: 3,
                route: item.clone(),
                revoked: false
            }),
            Err(RouteError::StaleVersion)
        );
        assert!(
            table
                .select(
                    item.tenant_id,
                    item.endpoint_id,
                    "connect",
                    SystemTime::now()
                )
                .is_ok()
        );
    }
}
