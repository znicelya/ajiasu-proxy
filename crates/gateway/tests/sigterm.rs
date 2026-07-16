#![cfg(unix)]

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
    let mut child = Command::new(env!("CARGO_BIN_EXE_ajiasu-gateway"))
        .env("AJIASU_GATEWAY_NAME", "sigterm-gateway")
        .env("AJIASU_GATEWAY_STATE_DIRECTORY", &root)
        .env(
            "AJIASU_GATEWAY_CONTROL_PLANE_ENDPOINT",
            "http://127.0.0.1:1",
        )
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
