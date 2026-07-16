package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/dnomd343/ajiasu-proxy/internal/platform/config"
	"github.com/dnomd343/ajiasu-proxy/internal/platform/database"
	"github.com/dnomd343/ajiasu-proxy/internal/testkit"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestComposeMaterializeIsPrivateRandomAndIdempotent(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "generated")
	registry := filepath.Join(t.TempDir(), "registry")
	options := materializeOptions{Output: directory, EnvironmentID: "phase7-test", Mode: "single-host", Registry: registry, Now: time.Date(2026, 7, 16, 8, 0, 0, 0, time.UTC)}
	if err := materializeCompose(options, func(string) (string, bool) { return "", false }); err != nil {
		t.Fatalf("materializeCompose() error = %v", err)
	}
	manifest, err := validateGeneratedState(directory, "phase7-test")
	if err != nil {
		t.Fatalf("validateGeneratedState() error = %v", err)
	}
	if manifest.GatewayFingerprint == "" || len(manifest.Files) != len(stableComposeFiles) {
		t.Fatalf("manifest = %#v", manifest)
	}
	before := snapshotGeneratedFiles(t, directory)
	options.Now = options.Now.Add(time.Hour)
	if err := materializeCompose(options, func(string) (string, bool) { return "", false }); err != nil {
		t.Fatalf("idempotent materializeCompose() error = %v", err)
	}
	after := snapshotGeneratedFiles(t, directory)
	if !equalStringMap(before, after) {
		t.Fatal("idempotent initialization rotated or rewrote generated material")
	}
	keyring := mustRead(t, filepath.Join(directory, "control-plane-keyring"))
	signing := mustRead(t, filepath.Join(directory, "route-signing-key"))
	verifying := mustRead(t, filepath.Join(directory, "route-verifying-key"))
	if len(keyring) != 32 || len(signing) != ed25519.SeedSize || len(verifying) != ed25519.PublicKeySize || bytes.Equal(keyring, signing) {
		t.Fatal("generated cryptographic material has invalid entropy, length, or collision")
	}
	if !bytes.Equal(ed25519.NewKeyFromSeed(signing).Public().(ed25519.PublicKey), verifying) {
		t.Fatal("route signing and verifying keys do not match")
	}
	if strings.Contains(string(mustRead(t, filepath.Join(directory, manifestName))), string(keyring)) {
		t.Fatal("manifest exposed raw secret material")
	}
	entries, _ := os.ReadDir(directory)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".partial-") {
			t.Fatalf("partial file survived initialization: %s", entry.Name())
		}
	}
}

func TestComposeMaterializeRejectsCollisionUnexpectedTamperAndWeakModes(t *testing.T) {
	registry := filepath.Join(t.TempDir(), "registry")
	first := filepath.Join(t.TempDir(), "first")
	options := materializeOptions{Output: first, EnvironmentID: "collision-test", Mode: "development", Registry: registry, Now: time.Now().UTC()}
	if err := materializeCompose(options, func(string) (string, bool) { return "", false }); err != nil {
		t.Fatal(err)
	}
	options.Output = filepath.Join(t.TempDir(), "second")
	if err := materializeCompose(options, func(string) (string, bool) { return "", false }); err == nil || !strings.Contains(err.Error(), "already claimed") {
		t.Fatalf("environment collision error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(first, "unexpected"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := validateGeneratedState(first, "collision-test"); err == nil || !strings.Contains(err.Error(), "unexpected") {
		t.Fatalf("unexpected-file validation error = %v", err)
	}
	if err := os.Remove(filepath.Join(first, "unexpected")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(first, "redis-password")
	if err := os.WriteFile(path, []byte("tampered\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := validateGeneratedState(first, "collision-test"); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("tamper validation error = %v", err)
	}
	if runtime.GOOS != "windows" {
		if err := os.Chmod(first, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := validateGeneratedState(first, "collision-test"); err == nil || !strings.Contains(err.Error(), "permissions") {
			t.Fatalf("permission validation error = %v", err)
		}
	}
}

func TestComposeMaterializeRejectsSymlinksAndExistingFiles(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(root, "generated")
	if err := os.Symlink(target, link); err == nil {
		err := materializeCompose(materializeOptions{Output: link, EnvironmentID: "link-test", Mode: "development", Now: time.Now()}, func(string) (string, bool) { return "", false })
		if err == nil || !strings.Contains(err.Error(), "safe directory") {
			t.Fatalf("symlink error = %v", err)
		}
	}
	existing := filepath.Join(root, "existing")
	if err := os.Mkdir(existing, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(existing, "keep"), []byte("user data"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := materializeCompose(materializeOptions{Output: existing, EnvironmentID: "existing-test", Mode: "development", Now: time.Now()}, func(string) (string, bool) { return "", false })
	if err == nil || !strings.Contains(err.Error(), "unexpected existing files") {
		t.Fatalf("existing-file error = %v", err)
	}
	if got := string(mustRead(t, filepath.Join(existing, "keep"))); got != "user data" {
		t.Fatalf("existing file changed to %q", got)
	}
}

func TestComposeExternalModeReadsOnlySecretFilesAndCLIIsRedacted(t *testing.T) {
	root := t.TempDir()
	paths := map[string]string{}
	for name, value := range map[string]string{
		"AJIASU_DATABASE_NORMAL_DSN_FILE":    "postgresql://ajiasu_normal:very-secret@db/ajiasu?sslmode=verify-full",
		"AJIASU_DATABASE_PLATFORM_DSN_FILE":  "postgresql://ajiasu_control:very-secret@db/ajiasu?sslmode=verify-full",
		"AJIASU_DATABASE_MIGRATION_DSN_FILE": "postgresql://migration:very-secret@db/ajiasu?sslmode=verify-full",
	} {
		path := filepath.Join(root, strings.ToLower(name))
		if err := os.WriteFile(path, []byte(value+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		paths[name] = path
	}
	lookup := func(name string) (string, bool) { value, ok := paths[name]; return value, ok }
	output := filepath.Join(root, "generated")
	var stdout, stderr bytes.Buffer
	handled, code := runComposeCLI([]string{"compose", "materialize", "--output", output, "--environment-id", "external-test", "--mode", "external"}, lookup, &stdout, &stderr)
	if !handled || code != 0 {
		t.Fatalf("CLI handled=%v code=%d stderr=%q", handled, code, stderr.String())
	}
	combined := stdout.String() + stderr.String()
	if strings.Contains(combined, "very-secret") || combined != "" {
		t.Fatalf("CLI exposed output: %q", combined)
	}
	if got := string(mustRead(t, filepath.Join(output, "database-normal-dsn"))); !strings.Contains(got, "very-secret") {
		t.Fatal("external DSN was not copied")
	}
}

func TestComposeExternalModeRejectsInsecureDatabaseTransport(t *testing.T) {
	root := t.TempDir()
	paths := map[string]string{}
	for _, name := range []string{"AJIASU_DATABASE_NORMAL_DSN_FILE", "AJIASU_DATABASE_PLATFORM_DSN_FILE", "AJIASU_DATABASE_MIGRATION_DSN_FILE"} {
		path := filepath.Join(root, strings.ToLower(name))
		if err := os.WriteFile(path, []byte("postgresql://role:secret@db/ajiasu?sslmode=disable\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		paths[name] = path
	}
	lookup := func(name string) (string, bool) { value, ok := paths[name]; return value, ok }
	err := materializeCompose(materializeOptions{Output: filepath.Join(root, "generated"), EnvironmentID: "insecure-test", Mode: "external", Now: time.Now()}, lookup)
	if err == nil || !strings.Contains(err.Error(), "sslmode=verify-full") {
		t.Fatalf("insecure transport error = %v", err)
	}
}

func TestAtomicPrivateWriteNeverOverwritesAndCleansTemporaryFile(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "secret")
	if err := atomicPrivateWrite(path, []byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := atomicPrivateWrite(path, []byte("second")); !errors.Is(err, os.ErrExist) {
		t.Fatalf("overwrite error = %v", err)
	}
	if got := string(mustRead(t, path)); got != "first" {
		t.Fatalf("target = %q", got)
	}
	entries, _ := os.ReadDir(directory)
	if len(entries) != 1 || entries[0].Name() != "secret" {
		t.Fatalf("entries = %#v", entries)
	}
}

func TestComposeRuntimeStatusAndDrainOnEmptyMigratedDatabase(t *testing.T) {
	postgres := testkit.StartPostgres(t)
	if _, err := executeMigration(t.Context(), config.Migration{DSN: postgres.AdminDSN, Directory: repositoryMigrationDirectory(t), Timeout: 2 * time.Minute}, "up"); err != nil {
		t.Fatal(err)
	}
	pool, err := pgxpool.New(t.Context(), postgres.AdminDSN)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	pools := &database.Pools{Platform: pool}
	status, err := readComposeRuntimeStatus(t.Context(), pools)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "token") || status.Nodes.Total != 0 || status.Gateways.Total != 0 {
		t.Fatalf("status = %s", encoded)
	}
	drained, err := drainComposeRuntime(t.Context(), pools)
	if err != nil {
		t.Fatal(err)
	}
	if drained["nodes"] != 0 || drained["assignments"] != 0 {
		t.Fatalf("drain = %#v", drained)
	}
}

func snapshotGeneratedFiles(t *testing.T, directory string) map[string]string {
	t.Helper()
	result := map[string]string{}
	entries, err := os.ReadDir(directory)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		value := mustRead(t, filepath.Join(directory, entry.Name()))
		digest := sha256.Sum256(value)
		result[entry.Name()] = hex.EncodeToString(digest[:])
	}
	return result
}

func equalStringMap(left, right map[string]string) bool {
	leftJSON, _ := json.Marshal(left)
	rightJSON, _ := json.Marshal(right)
	return bytes.Equal(leftJSON, rightJSON)
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	value, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
