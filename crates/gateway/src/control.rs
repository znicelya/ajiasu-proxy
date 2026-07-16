use async_trait::async_trait;
use thiserror::Error;

use crate::routes::{Delta, RouteError, RouteTable, Snapshot};

#[derive(Debug, Error)]
pub enum ControlError {
    #[error("control stream unavailable")]
    Unavailable,
    #[error(transparent)]
    Route(#[from] RouteError),
}

#[async_trait]
pub trait ControlTransport: Send {
    async fn next_snapshot(&mut self) -> Result<Option<ControlMessage>, ControlError>;
}
pub enum ControlMessage {
    Snapshot(Snapshot),
    Delta(Box<Delta>),
    Shutdown,
}

pub struct ControlClient<T> {
    transport: T,
    routes: RouteTable,
}
impl<T: ControlTransport> ControlClient<T> {
    pub fn new(transport: T, routes: RouteTable) -> Self {
        Self { transport, routes }
    }
    pub async fn run(mut self) -> Result<RouteTable, ControlError> {
        let mut needs_snapshot = false;
        while let Some(message) = self.transport.next_snapshot().await? {
            match message {
                ControlMessage::Snapshot(snapshot) => {
                    self.routes.apply_snapshot(snapshot)?;
                    needs_snapshot = false;
                }
                ControlMessage::Delta(delta) if !needs_snapshot => {
                    match self.routes.apply_delta(*delta) {
                        Ok(()) | Err(RouteError::StaleVersion | RouteError::StaleAssignment) => {}
                        Err(RouteError::SnapshotRequired) => needs_snapshot = true,
                        Err(error) => return Err(error.into()),
                    }
                }
                ControlMessage::Delta(_) => {}
                ControlMessage::Shutdown => break,
            }
        }
        Ok(self.routes)
    }
}

#[cfg(test)]
mod tests {
    use std::{collections::VecDeque, time::SystemTime};

    use super::*;
    use crate::routes::{Grant, Route};
    use uuid::Uuid;

    struct FakeTransport(VecDeque<ControlMessage>);

    #[async_trait]
    impl ControlTransport for FakeTransport {
        async fn next_snapshot(&mut self) -> Result<Option<ControlMessage>, ControlError> {
            Ok(self.0.pop_front())
        }
    }

    fn route(generation: u64) -> Route {
        let expiry = SystemTime::now() + std::time::Duration::from_secs(60);
        let runner_id = Uuid::from_u128(4);
        Route {
            tenant_id: Uuid::from_u128(1),
            endpoint_id: Uuid::from_u128(2),
            policy_hash: "hash".into(),
            protocols: vec!["connect".into()],
            runner_id,
            generation,
            assignment_id: Uuid::from_u128(3),
            assignment_generation: generation,
            account_id: Uuid::from_u128(5),
            node_id: Uuid::from_u128(6),
            assignment_state: "assigned".into(),
            valid_until: expiry,
            grant: Grant {
                gateway_id: Uuid::from_u128(7),
                tenant_id: Uuid::from_u128(1),
                endpoint_id: Uuid::from_u128(2),
                runner_id,
                generation,
                policy_hash: "hash".into(),
                expires_at: expiry,
                protocols: vec!["connect".into()],
                signature: vec![1],
            },
            credentials: vec![],
        }
    }

    #[tokio::test]
    async fn preserves_cache_until_recovery_snapshot_after_delta_gap() {
        let initial = route(1);
        let recovered = route(2);
        let transport = FakeTransport(VecDeque::from([
            ControlMessage::Snapshot(Snapshot {
                version: 1,
                routes: vec![initial.clone()],
            }),
            ControlMessage::Delta(Box::new(Delta {
                version: 3,
                route: recovered.clone(),
                revoked: false,
            })),
            ControlMessage::Delta(Box::new(Delta {
                version: 4,
                route: recovered.clone(),
                revoked: false,
            })),
            ControlMessage::Snapshot(Snapshot {
                version: 5,
                routes: vec![recovered.clone()],
            }),
            ControlMessage::Shutdown,
        ]));
        let table = ControlClient::new(transport, RouteTable::new())
            .run()
            .await
            .unwrap();
        assert_eq!(table.version(), 5);
        assert_eq!(
            table
                .select(
                    recovered.tenant_id,
                    recovered.endpoint_id,
                    "connect",
                    SystemTime::now()
                )
                .unwrap()
                .generation,
            2
        );
    }
}
