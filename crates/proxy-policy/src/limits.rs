use serde::{Deserialize, Serialize};

use crate::PolicyError;

#[derive(Clone, Debug, Eq, PartialEq, Serialize, Deserialize)]
#[serde(default, deny_unknown_fields)]
pub struct Limits {
    pub max_connections: u32,
    pub max_connection_rate: u32,
    pub idle_timeout_seconds: u32,
    pub max_bytes_per_connection: u64,
    pub traffic_window_seconds: u32,
    pub max_window_bytes: u64,
}

impl Default for Limits {
    fn default() -> Self {
        Self {
            max_connections: 100,
            max_connection_rate: 50,
            idle_timeout_seconds: 300,
            max_bytes_per_connection: 0,
            traffic_window_seconds: 0,
            max_window_bytes: 0,
        }
    }
}

impl Limits {
    pub fn validate(&self) -> Result<(), PolicyError> {
        if !(1..=100_000).contains(&self.max_connections)
            || !(1..=100_000).contains(&self.max_connection_rate)
            || !(1..=86_400).contains(&self.idle_timeout_seconds)
        {
            return Err(PolicyError::InvalidLimit);
        }
        if (self.traffic_window_seconds == 0) != (self.max_window_bytes == 0)
            || self.traffic_window_seconds > 86_400
        {
            return Err(PolicyError::InvalidTrafficWindow);
        }
        Ok(())
    }
}
