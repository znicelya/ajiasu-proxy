#![cfg(unix)]

use std::{
    fs,
    process::{Command, Stdio},
    thread,
    time::{Duration, Instant},
};

use ajiasu_agent::session::{self, SessionState};

#[test]
fn sigterm_completes_within_shutdown_deadline() {
    let root = std::env::temp_dir().join(format!("ajiasu-agent-sigterm-{}", uuid::Uuid::now_v7()));
    session::save(
        &root.join("session.json"),
        &SessionState {
            node_id: uuid::Uuid::now_v7().to_string(),
            session_token: "test-session".to_owned(),
            protocol_revision: 2,
        },
    )
    .unwrap();
    let mut child = Command::new(env!("CARGO_BIN_EXE_ajiasu-agent"))
        .env("AJIASU_AGENT_NODE_NAME", "sigterm-node")
        .env("AJIASU_AGENT_STATE_DIRECTORY", &root)
        .env("AJIASU_AGENT_CONTROL_PLANE_ENDPOINT", "http://127.0.0.1:1")
        .env("AJIASU_AGENT_RUNTIME", "process")
        .env("AJIASU_AGENT_SHUTDOWN_TIMEOUT", "2s")
        .stdout(Stdio::null())
        .stderr(Stdio::null())
        .spawn()
        .unwrap();
    thread::sleep(Duration::from_millis(200));
    let status = Command::new("kill")
        .args(["-TERM", &child.id().to_string()])
        .status()
        .unwrap();
    assert!(status.success());
    let deadline = Instant::now() + Duration::from_secs(4);
    loop {
        if let Some(status) = child.try_wait().unwrap() {
            assert!(status.success(), "agent exit status={status}");
            break;
        }
        assert!(
            Instant::now() < deadline,
            "agent did not honor shutdown deadline"
        );
        thread::sleep(Duration::from_millis(50));
    }
    fs::remove_dir_all(root).unwrap();
}
