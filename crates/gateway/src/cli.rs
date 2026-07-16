use std::{ffi::OsString, io::Write, path::PathBuf};

use serde_json::json;

use crate::session;

pub fn run_env(args: &[String]) -> Option<i32> {
    let mut stdout = std::io::stdout().lock();
    let mut stderr = std::io::stderr().lock();
    run(
        args,
        |name| std::env::var_os(name),
        &mut stdout,
        &mut stderr,
    )
}

pub fn run<F>(
    args: &[String],
    mut lookup: F,
    stdout: &mut dyn Write,
    stderr: &mut dyn Write,
) -> Option<i32>
where
    F: FnMut(&str) -> Option<OsString>,
{
    match args.first()?.as_str() {
        "version" => {
            if args.len() != 1 {
                let _ = writeln!(stderr, "gateway version does not accept arguments");
                return Some(2);
            }
            let _ = writeln!(
                stdout,
                "{}",
                json!({"component":"gateway","version":env!("CARGO_PKG_VERSION"),"protocol_revision":1})
            );
            Some(0)
        }
        "health" => {
            if args.len() != 2 || args[1] != "live" && args[1] != "ready" {
                let _ = writeln!(stderr, "usage: ajiasu-gateway health live|ready");
                return Some(2);
            }
            let directory = state_directory(&mut lookup);
            let healthy = if args[1] == "live" {
                directory.is_dir()
            } else {
                matches!(session::load(&directory.join("session.json")), Ok(Some(_)))
                    && crate::private_file::read(&directory.join("snapshot.ready"), 5, 5).is_ok()
            };
            if !healthy {
                let _ = writeln!(stderr, "gateway health check failed");
                return Some(1);
            }
            let _ = writeln!(
                stdout,
                "{}",
                json!({"component":"gateway","check":args[1],"status":"ok"})
            );
            Some(0)
        }
        "status" => {
            if args.len() != 1 {
                let _ = writeln!(stderr, "gateway status does not accept arguments");
                return Some(2);
            }
            let output = match session::load(&state_directory(&mut lookup).join("session.json")) {
                Ok(Some(state)) => {
                    json!({"component":"gateway","state":"enrolled","gateway_id":state.gateway_id,"protocol_revision":state.protocol_revision})
                }
                Ok(None) => json!({"component":"gateway","state":"unenrolled"}),
                Err(_) => {
                    let _ = writeln!(stderr, "gateway status is unavailable");
                    return Some(1);
                }
            };
            let _ = writeln!(stdout, "{output}");
            Some(0)
        }
        _ => None,
    }
}

fn state_directory<F>(lookup: &mut F) -> PathBuf
where
    F: FnMut(&str) -> Option<OsString>,
{
    lookup("AJIASU_GATEWAY_STATE_DIRECTORY")
        .map(PathBuf::from)
        .unwrap_or_else(|| PathBuf::from("/var/lib/ajiasu-gateway"))
}

#[cfg(test)]
mod tests {
    use std::fs;

    use super::*;
    use crate::session::SessionState;

    #[test]
    fn version_is_local_and_status_is_secret_free() {
        let mut stdout = Vec::new();
        let mut stderr = Vec::new();
        assert_eq!(
            run(
                &["version".to_owned()],
                |_| panic!("version loaded environment"),
                &mut stdout,
                &mut stderr
            ),
            Some(0)
        );
        assert!(
            String::from_utf8(stdout)
                .unwrap()
                .contains("protocol_revision")
        );

        let root =
            std::env::temp_dir().join(format!("ajiasu-gateway-cli-{}", uuid::Uuid::now_v7()));
        session::save(
            &root.join("session.json"),
            &SessionState {
                gateway_id: "gateway-a".to_owned(),
                session_token: "gateway-token-canary".to_owned(),
                protocol_revision: 1,
                gateway_instance_id: uuid::Uuid::now_v7().to_string(),
            },
        )
        .unwrap();
        crate::private_file::atomic_write(&root.join("snapshot.ready"), b"ready").unwrap();
        let mut stdout = Vec::new();
        assert_eq!(
            run(
                &["status".to_owned()],
                |name| (name == "AJIASU_GATEWAY_STATE_DIRECTORY")
                    .then(|| root.clone().into_os_string()),
                &mut stdout,
                &mut stderr
            ),
            Some(0)
        );
        assert!(
            !String::from_utf8(stdout)
                .unwrap()
                .contains("gateway-token-canary")
        );
        fs::remove_dir_all(root).unwrap();
    }
}
