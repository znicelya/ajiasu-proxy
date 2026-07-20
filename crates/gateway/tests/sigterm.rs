#![cfg(unix)]

use ajiasu_gateway::session::{self, SessionState};
use ed25519_dalek::SigningKey;
use std::{
    fs,
    process::{Command, Stdio},
    thread,
    time::{Duration, Instant},
};

#[test]
fn sigterm_completes_within_shutdown_deadline() {
    let root =
        std::env::temp_dir().join(format!("ajiasu-gateway-sigterm-{}", uuid::Uuid::now_v7()));
    fs::create_dir_all(&root).unwrap();
    let verifying_key = root.join("route-verifying-key");
    fs::write(
        &verifying_key,
        SigningKey::from_bytes(&[7; 32]).verifying_key().to_bytes(),
    )
    .unwrap();
    session::save(
        &root.join("session.json"),
        &SessionState {
            gateway_id: "sigterm-gateway".to_owned(),
            gateway_instance_id: uuid::Uuid::now_v7().to_string(),
            session_token: "sigterm-session-token".to_owned(),
            protocol_revision: 1,
        },
    )
    .unwrap();
    let mut child = Command::new(env!("CARGO_BIN_EXE_ajiasu-gateway"))
        .env("AJIASU_GATEWAY_NAME", "sigterm-gateway")
        .env("AJIASU_GATEWAY_STATE_DIRECTORY", &root)
        .env(
            "AJIASU_GATEWAY_CONTROL_PLANE_ENDPOINT",
            "http://127.0.0.1:1",
        )
        .env(
            "AJIASU_GATEWAY_CERTIFICATE_FINGERPRINT",
            "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef",
        )
        .env("AJIASU_GATEWAY_RELAY_ENDPOINT", "http://127.0.0.1:1")
        .env("AJIASU_GATEWAY_ROUTE_VERIFYING_KEY_FILE", &verifying_key)
        .env("AJIASU_GATEWAY_SHUTDOWN_TIMEOUT", "2s")
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
            assert!(status.success(), "gateway exit status={status}");
            break;
        }
        assert!(
            Instant::now() < deadline,
            "gateway did not honor shutdown deadline"
        );
        thread::sleep(Duration::from_millis(50));
    }
    fs::remove_dir_all(root).unwrap();
}
