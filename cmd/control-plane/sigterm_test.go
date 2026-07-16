//go:build !windows

package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestControlPlaneSIGTERMCompletesWithinDeadline(t *testing.T) {
	root := t.TempDir()
	binary := filepath.Join(root, "control-plane")
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build control-plane: %v\n%s", err, output)
	}
	write := func(name, value string) string {
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	environment := append(os.Environ(),
		"AJIASU_ENVIRONMENT=development",
		"AJIASU_ENVIRONMENT_ID=sigterm-test",
		"AJIASU_HTTP_BIND=127.0.0.1:0",
		"AJIASU_HTTP_READ_HEADER_TIMEOUT=1s",
		"AJIASU_HTTP_READ_TIMEOUT=1s",
		"AJIASU_HTTP_WRITE_TIMEOUT=1s",
		"AJIASU_HTTP_IDLE_TIMEOUT=1s",
		"AJIASU_HTTP_SHUTDOWN_TIMEOUT=2s",
		"AJIASU_AGENT_GRPC_BIND=127.0.0.1:0",
		"AJIASU_AGENT_GRPC_INSECURE=true",
		"AJIASU_GATEWAY_GRPC_BIND=127.0.0.1:0",
		"AJIASU_GATEWAY_GRPC_INSECURE=true",
		"AJIASU_DATABASE_NORMAL_DSN_FILE="+write("normal-dsn", "postgres://normal:test@127.0.0.1:1/test?sslmode=disable"),
		"AJIASU_DATABASE_NORMAL_MAX_OPEN=2",
		"AJIASU_DATABASE_NORMAL_MIN_IDLE=0",
		"AJIASU_DATABASE_PLATFORM_DSN_FILE="+write("platform-dsn", "postgres://platform:test@127.0.0.1:1/test?sslmode=disable"),
		"AJIASU_DATABASE_PLATFORM_MAX_OPEN=2",
		"AJIASU_DATABASE_PLATFORM_MIN_IDLE=0",
		"AJIASU_REDIS_ADDRESS=127.0.0.1:1",
		"AJIASU_REDIS_USERNAME=scheduler",
		"AJIASU_REDIS_PASSWORD_FILE="+write("redis-password", "test-password"),
		"AJIASU_REDIS_DATABASE=0",
		"AJIASU_REDIS_TLS=false",
		"AJIASU_REDIS_OPERATION_TIMEOUT=1s",
		"AJIASU_SCHEDULER_LEASE_NAMESPACE=ajiasu:sigterm:v1",
		"AJIASU_SCHEDULER_LEASE_TTL=9s",
		"AJIASU_SCHEDULER_LEASE_RENEW_INTERVAL=2s",
		"AJIASU_OIDC_ISSUER=https://issuer.example.test",
		"AJIASU_OIDC_CLIENT_ID=control-plane",
		"AJIASU_OIDC_CLIENT_SECRET_FILE="+write("oidc-secret", "test-client-secret"),
		"AJIASU_OIDC_REDIRECT_URL=https://proxy.example.test/callback",
		"AJIASU_SESSION_COOKIE_NAME=session",
		"AJIASU_SESSION_COOKIE_SECURE=false",
		"AJIASU_SESSION_IDLE_TIMEOUT=30m",
		"AJIASU_SESSION_ABSOLUTE_TIMEOUT=12h",
		"AJIASU_KEYRING_FILE="+write("keyring", "0123456789abcdef0123456789abcdef"),
		"AJIASU_ROUTE_SIGNING_KEY_FILE="+write("route-signing-key", "0123456789abcdef0123456789abcdef"),
		"AJIASU_LOCAL_AUTH_ENABLED=true",
		"AJIASU_LOCAL_AUTH_ALLOWED_CIDRS=127.0.0.0/8,::1/128",
	)
	var stdout, stderr bytes.Buffer
	command := exec.Command(binary)
	command.Env = environment
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if err := command.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- command.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("control-plane exit: %v\nstdout=%s\nstderr=%s", err, stdout.String(), stderr.String())
		}
	case <-time.After(5 * time.Second):
		_ = command.Process.Kill()
		t.Fatal("control-plane did not honor shutdown deadline")
	}
}
