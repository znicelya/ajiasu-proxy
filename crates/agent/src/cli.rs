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
    let command = args.first()?.as_str();
    match command {
        "version" => {
            if args.len() != 1 {
                let _ = writeln!(stderr, "agent version does not accept arguments");
                return Some(2);
            }
            let output = json!({
                "component": "agent",
                "version": env!("CARGO_PKG_VERSION"),
                "protocol_revision": ajiasu_agent_protocol::CURRENT_PROTOCOL_REVISION,
            });
            let _ = writeln!(stdout, "{output}");
            Some(0)
        }
        "health" => {
            if args.len() != 2 || args[1] != "live" && args[1] != "ready" {
                let _ = writeln!(stderr, "usage: ajiasu-agent health live|ready");
                return Some(2);
            }
            let directory = state_directory(&mut lookup);
            let healthy = if args[1] == "live" {
                directory.is_dir()
            } else {
                matches!(session::load(&directory.join("session.json")), Ok(Some(_)))
            };
            if !healthy {
                let _ = writeln!(stderr, "agent health check failed");
                return Some(1);
            }
            let _ = writeln!(
                stdout,
                "{}",
                json!({"component":"agent","check":args[1],"status":"ok"})
            );
            Some(0)
        }
        "status" => {
            if args.len() != 1 {
                let _ = writeln!(stderr, "agent status does not accept arguments");
                return Some(2);
            }
            let directory = state_directory(&mut lookup);
            let output = match session::load(&directory.join("session.json")) {
                Ok(Some(state)) => json!({
                    "component": "agent",
                    "state": "enrolled",
                    "node_id": state.node_id,
                    "protocol_revision": state.protocol_revision,
                }),
                Ok(None) => json!({"component":"agent","state":"unenrolled"}),
                Err(_) => {
                    let _ = writeln!(stderr, "agent status is unavailable");
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
    lookup("AJIASU_AGENT_STATE_DIRECTORY")
        .map(PathBuf::from)
        .unwrap_or_else(|| PathBuf::from("/var/lib/ajiasu-agent"))
}

#[cfg(test)]
mod tests {
    use std::fs;

    use super::*;
    use crate::session::SessionState;

    #[test]
    fn version_does_not_load_configuration() {
        let mut stdout = Vec::new();
        let mut stderr = Vec::new();
        let result = run(
            &["version".to_owned()],
            |_| panic!("version loaded environment"),
            &mut stdout,
            &mut stderr,
        );
        assert_eq!(result, Some(0));
        assert!(
            String::from_utf8(stdout)
                .unwrap()
                .contains("protocol_revision")
        );
        assert!(stderr.is_empty());
    }

    #[test]
    fn status_and_health_never_expose_session_token() {
        let root = std::env::temp_dir().join(format!("ajiasu-agent-cli-{}", uuid::Uuid::now_v7()));
        session::save(
            &root.join("session.json"),
            &SessionState {
                node_id: "node-a".to_owned(),
                session_token: "session-token-canary".to_owned(),
                protocol_revision: 2,
            },
        )
        .unwrap();
        let lookup = |name: &str| {
            (name == "AJIASU_AGENT_STATE_DIRECTORY").then(|| root.clone().into_os_string())
        };
        for args in [vec!["status"], vec!["health", "ready"]] {
            let mut stdout = Vec::new();
            let mut stderr = Vec::new();
            let args: Vec<String> = args.into_iter().map(str::to_owned).collect();
            let result = run(&args, lookup, &mut stdout, &mut stderr);
            let output = String::from_utf8(stdout).unwrap();
            assert_eq!(result, Some(0));
            assert!(!output.contains("session-token-canary"));
            assert!(stderr.is_empty());
        }
        fs::remove_dir_all(root).unwrap();
    }
}
