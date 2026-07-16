use std::{
    fs::{self, OpenOptions},
    io::{Read, Write},
    path::Path,
};

use thiserror::Error;

#[derive(Debug, Error)]
#[error("private file is unavailable")]
pub struct PrivateFileError;

pub fn read(path: &Path, minimum: usize, maximum: usize) -> Result<Vec<u8>, PrivateFileError> {
    let before = fs::symlink_metadata(path).map_err(|_| PrivateFileError)?;
    if minimum > maximum
        || before.file_type().is_symlink()
        || !before.is_file()
        || before.len() < minimum as u64
        || before.len() > maximum as u64
        || !private_permissions(&before)
    {
        return Err(PrivateFileError);
    }
    let mut file = OpenOptions::new()
        .read(true)
        .open(path)
        .map_err(|_| PrivateFileError)?;
    let after = file.metadata().map_err(|_| PrivateFileError)?;
    if !after.is_file() || !same_file(&before, &after) {
        return Err(PrivateFileError);
    }
    let mut content = Vec::with_capacity(before.len() as usize);
    Read::by_ref(&mut file)
        .take(maximum as u64 + 1)
        .read_to_end(&mut content)
        .map_err(|_| PrivateFileError)?;
    if content.len() < minimum || content.len() > maximum {
        content.fill(0);
        return Err(PrivateFileError);
    }
    Ok(content)
}

pub fn trim_nonempty(mut content: Vec<u8>) -> Result<Vec<u8>, PrivateFileError> {
    let start = content
        .iter()
        .position(|byte| !byte.is_ascii_whitespace())
        .ok_or(PrivateFileError)?;
    let end = content
        .iter()
        .rposition(|byte| !byte.is_ascii_whitespace())
        .ok_or(PrivateFileError)?
        + 1;
    let result = content[start..end].to_vec();
    content.fill(0);
    Ok(result)
}

pub fn atomic_write(path: &Path, content: &[u8]) -> Result<(), PrivateFileError> {
    let parent = path.parent().ok_or(PrivateFileError)?;
    fs::create_dir_all(parent).map_err(|_| PrivateFileError)?;
    let name = path
        .file_name()
        .and_then(|name| name.to_str())
        .ok_or(PrivateFileError)?;
    let mut temporary = None;
    for attempt in 0..16_u8 {
        let candidate = parent.join(format!(".{name}.{}.{}.tmp", std::process::id(), attempt));
        let mut options = OpenOptions::new();
        options.write(true).create_new(true);
        set_private_create_mode(&mut options);
        match options.open(&candidate) {
            Ok(file) => {
                temporary = Some((candidate, file));
                break;
            }
            Err(error) if error.kind() == std::io::ErrorKind::AlreadyExists => continue,
            Err(_) => return Err(PrivateFileError),
        }
    }
    let (temporary_path, mut file) = temporary.ok_or(PrivateFileError)?;
    let result = (|| {
        file.write_all(content).map_err(|_| PrivateFileError)?;
        file.sync_all().map_err(|_| PrivateFileError)?;
        drop(file);
        #[cfg(windows)]
        if path.exists() {
            fs::remove_file(path).map_err(|_| PrivateFileError)?;
        }
        fs::rename(&temporary_path, path).map_err(|_| PrivateFileError)?;
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
