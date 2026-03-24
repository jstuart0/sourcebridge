//! Data validation and transformation module.
//!
//! REQ-008: Core validation utilities
//! REQ-009: Data transformation pipeline

/// Validates that a string is a valid identifier.
/// REQ-008: Identifier validation rules
pub fn is_valid_identifier(s: &str) -> bool {
    if s.is_empty() {
        return false;
    }
    let mut chars = s.chars();
    let first = chars.next().unwrap();
    if !first.is_alphabetic() && first != '_' {
        return false;
    }
    chars.all(|c| c.is_alphanumeric() || c == '_')
}

/// Transforms text to a normalized form.
/// REQ-009: Text normalization
pub fn normalize(text: &str) -> String {
    text.trim()
        .to_lowercase()
        .replace(|c: char| !c.is_alphanumeric() && c != ' ', "")
        .split_whitespace()
        .collect::<Vec<&str>>()
        .join(" ")
}

/// Calculates a simple checksum for data integrity.
/// REQ-015: Data integrity validation
pub fn checksum(data: &[u8]) -> u32 {
    let mut hash: u32 = 0;
    for &byte in data {
        hash = hash.wrapping_mul(31).wrapping_add(byte as u32);
    }
    hash
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn test_valid_identifiers() {
        assert!(is_valid_identifier("hello"));
        assert!(is_valid_identifier("_private"));
        assert!(is_valid_identifier("name_123"));
        assert!(!is_valid_identifier(""));
        assert!(!is_valid_identifier("123abc"));
        assert!(!is_valid_identifier("no spaces"));
    }

    #[test]
    fn test_normalize() {
        assert_eq!(normalize("  Hello  World  "), "hello world");
        assert_eq!(normalize("Test!@#Case"), "testcase");
    }

    #[test]
    fn test_checksum_deterministic() {
        let data = b"hello world";
        assert_eq!(checksum(data), checksum(data));
    }

    #[test]
    fn test_checksum_different() {
        assert_ne!(checksum(b"hello"), checksum(b"world"));
    }
}
