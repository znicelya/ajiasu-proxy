use zeroize::{Zeroize, ZeroizeOnDrop};

#[derive(Zeroize, ZeroizeOnDrop)]
pub struct SecretBytes(Vec<u8>);

impl SecretBytes {
    pub fn new(value: Vec<u8>) -> Self {
        Self(value)
    }

    pub fn expose(&self) -> &[u8] {
        &self.0
    }
}

impl std::fmt::Debug for SecretBytes {
    fn fmt(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        formatter.write_str("SecretBytes([redacted])")
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn debug_output_is_redacted() {
        let value = SecretBytes::new(b"credential-canary".to_vec());
        let output = format!("{value:?}");
        assert_eq!(output, "SecretBytes([redacted])");
        assert!(!output.contains("credential-canary"));
        assert_eq!(value.expose(), b"credential-canary");
    }
}
