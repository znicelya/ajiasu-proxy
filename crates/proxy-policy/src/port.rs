use serde::{Deserialize, Serialize};

use crate::PolicyError;

#[derive(Clone, Copy, Debug, Eq, PartialEq, Ord, PartialOrd, Serialize, Deserialize)]
#[serde(deny_unknown_fields)]
pub struct PortRange {
    pub from: u16,
    pub to: u16,
}

impl PortRange {
    pub fn validate(self) -> Result<Self, PolicyError> {
        if self.from == 0 || self.to == 0 || self.from > self.to {
            return Err(PolicyError::InvalidPortRange);
        }
        Ok(self)
    }

    pub fn contains(self, port: u16) -> bool {
        port >= self.from && port <= self.to
    }
}

pub(crate) fn normalize(mut ranges: Vec<PortRange>) -> Result<Vec<PortRange>, PolicyError> {
    for range in &ranges {
        range.validate()?;
    }
    ranges.sort_unstable();
    let mut normalized: Vec<PortRange> = Vec::with_capacity(ranges.len());
    for range in ranges {
        if let Some(previous) = normalized.last_mut()
            && range.from <= previous.to.saturating_add(1)
        {
            previous.to = previous.to.max(range.to);
            continue;
        }
        normalized.push(range);
    }
    Ok(normalized)
}
