use crate::PolicyError;

pub fn canonicalize(value: &str) -> Result<String, PolicyError> {
    let value = value.trim().trim_end_matches('.');
    if value.is_empty() || value.contains('*') {
        return Err(PolicyError::InvalidDomain);
    }
    let ascii = idna::domain_to_ascii(value).map_err(|_| PolicyError::InvalidDomain)?;
    let ascii = ascii.to_ascii_lowercase();
    if ascii.len() > 253
        || ascii.split('.').any(|label| {
            label.is_empty() || label.len() > 63 || label.starts_with('-') || label.ends_with('-')
        })
    {
        return Err(PolicyError::InvalidDomain);
    }
    Ok(ascii)
}

pub fn matches(rule: &str, host: &str) -> bool {
    host == rule
        || host
            .strip_suffix(rule)
            .is_some_and(|prefix| prefix.ends_with('.'))
}
