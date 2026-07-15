#![forbid(unsafe_code)]

pub mod v1 {
    tonic::include_proto!("ajiasu.agent.v1");
}

pub const CURRENT_PROTOCOL_REVISION: u32 = 2;
pub const PREVIOUS_PROTOCOL_REVISION: u32 = 1;

pub fn negotiate_revision(minimum: u32, maximum: u32) -> Option<u32> {
    [CURRENT_PROTOCOL_REVISION, PREVIOUS_PROTOCOL_REVISION]
        .into_iter()
        .find(|revision| *revision >= minimum && *revision <= maximum)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn negotiates_current_then_previous_and_rejects_unknown_ranges() {
        assert_eq!(negotiate_revision(1, 2), Some(2));
        assert_eq!(negotiate_revision(1, 1), Some(1));
        assert_eq!(negotiate_revision(3, 4), None);
        assert_eq!(negotiate_revision(0, 0), None);
    }
}
