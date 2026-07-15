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
        while let Some(message) = self.transport.next_snapshot().await? {
            match message {
                ControlMessage::Snapshot(snapshot) => self.routes.apply_snapshot(snapshot)?,
                ControlMessage::Delta(delta) => self.routes.apply_delta(*delta)?,
                ControlMessage::Shutdown => break,
            }
        }
        Ok(self.routes)
    }
}
