#![forbid(unsafe_code)]

pub mod gateway_v1 {
    tonic::include_proto!("ajiasu.gateway.v1");
}

pub mod relay_v1 {
    tonic::include_proto!("ajiasu.relay.v1");
}

pub const CURRENT_GATEWAY_PROTOCOL_REVISION: u32 = 1;
pub const PREVIOUS_GATEWAY_PROTOCOL_REVISION: u32 = 1;

pub fn negotiate_gateway_revision(minimum: u32, maximum: u32) -> Option<u32> {
    (minimum <= CURRENT_GATEWAY_PROTOCOL_REVISION && maximum >= CURRENT_GATEWAY_PROTOCOL_REVISION)
        .then_some(CURRENT_GATEWAY_PROTOCOL_REVISION)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn accepts_revision_one_and_rejects_unknown_ranges() {
        assert_eq!(negotiate_gateway_revision(1, 1), Some(1));
        assert_eq!(negotiate_gateway_revision(0, 1), Some(1));
        assert_eq!(negotiate_gateway_revision(2, 3), None);
    }
}
