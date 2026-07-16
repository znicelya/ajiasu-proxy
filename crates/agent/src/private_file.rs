use std::{
    fs::{self, OpenOptions},
    io::{Read, Write},
    path::Path,
};

use thiserror::Error;

#[derive(Debug, Error)]
pub enum PrivateFileError {
    #[error("private file is unavailable")]
    Invalid,
}

pub fn read(path: &Path, minimum: usize, maximum: usize) -> Result<Vec<u8>, PrivateFileError> {
    if minimum > maximum {
        return Err(PrivateFileError::Invalid);
    }
    let before = fs::symlink_metadata(path).map_err(|_| PrivateFileError::Invalid)?;
    if before.file_type().is_symlink()
        || !before.is_file()
        || before.len() < minimum as u64
        || before.len() > maximum as u64
        || !private_permissions(&before)
    {
        return Err(PrivateFileError::Invalid);
    }
    let mut file = OpenOptions::new()
        .read(true)
        .open(path)
        .map_err(|_| PrivateFileError::Invalid)?;
    let after = file.metadata().map_err(|_| PrivateFileError::Invalid)?;
    if !after.is_file() || !same_file(&before, &after) {
        return Err(PrivateFileError::Invalid);
    }
    let mut content = Vec::with_capacity(before.len() as usize);
    Read::by_ref(&mut file)
        .take(maximum as u64 + 1)
        .read_to_end(&mut content)
        .map_err(|_| PrivateFileError::Invalid)?;
    if content.len() < minimum || content.len() > maximum {
        content.fill(0);
        return Err(PrivateFileError::Invalid);
    }
    Ok(content)
}

pub fn trim_nonempty(mut content: Vec<u8>) -> Result<Vec<u8>, PrivateFileError> {
    let start = content
        .iter()
        .position(|byte| !byte.is_ascii_whitespace())
        .ok_or(PrivateFileError::Invalid)?;
    let end = content
        .iter()
        .rposition(|byte| !byte.is_ascii_whitespace())
        .ok_or(PrivateFileError::Invalid)?
        + 1;
    let result = content[start..end].to_vec();
    content.fill(0);
    Ok(result)
}

pub fn atomic_write(path: &Path, content: &[u8]) -> Result<(), PrivateFileError> {
    let parent = path.parent().ok_or(PrivateFileError::Invalid)?;
    fs::create_dir_all(parent).map_err(|_| PrivateFileError::Invalid)?;
    let file_name = path
        .file_name()
        .and_then(|name| name.to_str())
        .ok_or(PrivateFileError::Invalid)?;
    let mut temporary = None;
    for attempt in 0..16_u8 {
        let candidate = parent.join(format!(
            ".{file_name}.{}.{}.tmp",
            std::process::id(),
            attempt
        ));
        let mut options = OpenOptions::new();
        options.write(true).create_new(true);
        set_private_create_mode(&mut options);
        match options.open(&candidate) {
            Ok(file) => {
                temporary = Some((candidate, file));
                break;
            }
            Err(error) if error.kind() == std::io::ErrorKind::AlreadyExists => continue,
            Err(_) => return Err(PrivateFileError::Invalid),
        }
    }
    let (temporary_path, mut file) = temporary.ok_or(PrivateFileError::Invalid)?;
    let result = (|| {
        file.write_all(content)
            .map_err(|_| PrivateFileError::Invalid)?;
        file.sync_all().map_err(|_| PrivateFileError::Invalid)?;
        drop(file);
        #[cfg(windows)]
        if path.exists() {
            fs::remove_file(path).map_err(|_| PrivateFileError::Invalid)?;
        }
        fs::rename(&temporary_path, path).map_err(|_| PrivateFileError::Invalid)?;
        Ok(())
    })();
    if result.is_err() {
        let _ = fs::remove_file(temporary_path);
    }
    result
}

#[cfg(unix)]
fn private_permissions(metadata: &fs::Metadata) -> bool {
    use std::os::unix::fs::PermissionsExt;
    metadata.permissions().mode() & 0o077 == 0
}

#[cfg(not(unix))]
fn private_permissions(_: &fs::Metadata) -> bool {
    true
}

#[cfg(unix)]
fn same_file(left: &fs::Metadata, right: &fs::Metadata) -> bool {
    use std::os::unix::fs::MetadataExt;
    left.dev() == right.dev() && left.ino() == right.ino()
}

#[cfg(not(unix))]
fn same_file(_: &fs::Metadata, _: &fs::Metadata) -> bool {
    true
}

#[cfg(unix)]
fn set_private_create_mode(options: &mut OpenOptions) {
    use std::os::unix::fs::OpenOptionsExt;
    options.mode(0o600);
}

#[cfg(not(unix))]
fn set_private_create_mode(_: &mut OpenOptions) {}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn rejects_directory_whitespace_and_oversize() {
        let root =
            std::env::temp_dir().join(format!("ajiasu-agent-private-{}", uuid::Uuid::now_v7()));
        fs::create_dir_all(&root).unwrap();
        let directory = root.join("directory");
        fs::create_dir(&directory).unwrap();
        assert!(read(&directory, 1, 16).is_err());
        let whitespace = root.join("whitespace");
        atomic_write(&whitespace, b" \r\n\t").unwrap();
        assert!(trim_nonempty(read(&whitespace, 1, 16).unwrap()).is_err());
        let oversize = root.join("oversize");
        atomic_write(&oversize, b"12345").unwrap();
        assert!(read(&oversize, 1, 4).is_err());
        fs::remove_dir_all(root).unwrap();
    }

    #[cfg(unix)]
    #[test]
    fn rejects_symbolic_link_and_broad_permissions() {
        use std::os::unix::fs::{PermissionsExt, symlink};
        let root = std::env::temp_dir().join(format!("ajiasu-agent-link-{}", uuid::Uuid::now_v7()));
        fs::create_dir_all(&root).unwrap();
        let target = root.join("target");
        atomic_write(&target, b"secret").unwrap();
        let link = root.join("link");
        symlink(&target, &link).unwrap();
        assert!(read(&link, 1, 16).is_err());
        fs::set_permissions(&target, fs::Permissions::from_mode(0o640)).unwrap();
        assert!(read(&target, 1, 16).is_err());
        fs::remove_dir_all(root).unwrap();
    }
}
