package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/dnomd343/ajiasu-proxy/internal/audit"
	"github.com/dnomd343/ajiasu-proxy/internal/gateways"
	"github.com/dnomd343/ajiasu-proxy/internal/nodes"
	"github.com/dnomd343/ajiasu-proxy/internal/platform/config"
	"github.com/dnomd343/ajiasu-proxy/internal/platform/database"
	"github.com/dnomd343/ajiasu-proxy/internal/tenancy"
	"github.com/google/uuid"
)

const composeHelp = `Usage:
  control-plane compose materialize --output DIR --environment-id ID --mode development|single-host|external
  control-plane compose validate --output DIR --environment-id ID
  control-plane compose enroll-agent --name NAME --output FILE
  control-plane compose enroll-gateway --name NAME --fingerprint-file FILE --output FILE

Secret values are generated or read through *_FILE environment variables. They are never accepted as flags.`

const manifestName = "generated-state.json"

var stableComposeFiles = []string{
	"agent-cert.pem", "agent-key.pem", "agent-relay-cert.pem", "agent-relay-key.pem",
	"control-plane-cert.pem", "control-plane-key.pem", "control-plane-keyring",
	"database-migration-dsn", "database-normal-dsn", "database-normal-password",
	"database-platform-dsn", "database-platform-password", "gateway-cert.pem", "gateway-key.pem",
	"gateway-certificate-fingerprint",
	"keycloak-bootstrap-password", "oidc-client-secret", "platform-ca.pem", "postgres-password",
	"redis-acl", "redis-password", "route-signing-key", "route-verifying-key",
}

var ephemeralComposeFiles = map[string]bool{
	"agent-enrollment-token": true, "gateway-enrollment-token": true,
}

type generatedFile struct {
	SHA256    string `json:"sha256"`
	Size      int64  `json:"size"`
	Sensitive bool   `json:"sensitive"`
}

type generatedManifest struct {
	SchemaVersion      int                      `json:"schema_version"`
	EnvironmentID      string                   `json:"environment_id"`
	Mode               string                   `json:"mode"`
	CreatedAt          time.Time                `json:"created_at"`
	GatewayFingerprint string                   `json:"gateway_certificate_fingerprint"`
	Files              map[string]generatedFile `json:"files"`
	EphemeralFiles     []string                 `json:"ephemeral_files"`
}

type materializeOptions struct {
	Output, EnvironmentID, Mode, Registry string
	Now                                   time.Time
}

func runComposeCLI(args []string, lookup func(string) (string, bool), stdout, stderr io.Writer) (bool, int) {
	if len(args) == 0 || args[0] != "compose" {
		return false, 0
	}
	if len(args) == 1 || args[1] == "--help" || args[1] == "-h" {
		_, _ = fmt.Fprintln(stdout, composeHelp)
		return true, 0
	}
	var err error
	switch args[1] {
	case "materialize":
		fs := flag.NewFlagSet("compose materialize", flag.ContinueOnError)
		fs.SetOutput(stderr)
		output := fs.String("output", "", "generated-state directory")
		environmentID := fs.String("environment-id", "", "deployment identifier")
		mode := fs.String("mode", "", "development, single-host, or external")
		registry := fs.String("registry", "", "environment identifier registry")
		if parseErr := fs.Parse(args[2:]); parseErr != nil || fs.NArg() != 0 {
			return true, 2
		}
		err = materializeCompose(materializeOptions{Output: *output, EnvironmentID: *environmentID, Mode: *mode, Registry: *registry, Now: time.Now().UTC()}, lookup)
	case "validate":
		fs := flag.NewFlagSet("compose validate", flag.ContinueOnError)
		fs.SetOutput(stderr)
		output := fs.String("output", "", "generated-state directory")
		environmentID := fs.String("environment-id", "", "deployment identifier")
		if parseErr := fs.Parse(args[2:]); parseErr != nil || fs.NArg() != 0 {
			return true, 2
		}
		_, err = validateGeneratedState(*output, *environmentID)
	case "enroll-agent":
		err = executeComposeAgentEnrollment(context.Background(), args[2:], lookup, stdout, stderr)
	case "enroll-gateway":
		err = executeComposeGatewayEnrollment(context.Background(), args[2:], lookup, stdout, stderr)
	default:
		_, _ = fmt.Fprintln(stderr, "unknown compose command")
		return true, 2
	}
	if err != nil {
		_, _ = fmt.Fprintln(stderr, "compose command failed:", err)
		return true, 1
	}
	return true, 0
}

func materializeCompose(options materializeOptions, lookup func(string) (string, bool)) error {
	options.Output = filepath.Clean(strings.TrimSpace(options.Output))
	if options.Output == "." || options.Output == "" || !validEnvironmentID(options.EnvironmentID) {
		return errors.New("output and a lowercase environment ID are required")
	}
	if options.Mode != "development" && options.Mode != "single-host" && options.Mode != "external" {
		return errors.New("mode must be development, single-host, or external")
	}
	if options.Mode == "external" {
		expectedUsers := map[string]string{"AJIASU_DATABASE_NORMAL_DSN_FILE": "ajiasu_normal", "AJIASU_DATABASE_PLATFORM_DSN_FILE": "ajiasu_control", "AJIASU_DATABASE_MIGRATION_DSN_FILE": ""}
		for _, name := range []string{"AJIASU_DATABASE_NORMAL_DSN_FILE", "AJIASU_DATABASE_PLATFORM_DSN_FILE", "AJIASU_DATABASE_MIGRATION_DSN_FILE"} {
			value, readErr := readRequiredSecretFile(lookup, name)
			if readErr != nil {
				return readErr
			}
			parsed, parseErr := url.Parse(strings.TrimSpace(string(value)))
			if parseErr != nil || (parsed.Scheme != "postgres" && parsed.Scheme != "postgresql") || parsed.Query().Get("sslmode") != "verify-full" {
				return fmt.Errorf("%s must use PostgreSQL sslmode=verify-full in external mode", name)
			}
			if expected := expectedUsers[name]; expected != "" {
				username := ""
				if parsed.User != nil {
					username = parsed.User.Username()
				}
				if username != expected {
					return fmt.Errorf("%s must use the package login role %s", name, expected)
				}
			}
		}
	}
	if options.Now.IsZero() {
		options.Now = time.Now().UTC()
	}
	if _, err := os.Lstat(filepath.Join(options.Output, manifestName)); err == nil {
		manifest, validateErr := validateGeneratedState(options.Output, options.EnvironmentID)
		if validateErr != nil {
			return validateErr
		}
		if manifest.Mode != options.Mode {
			return errors.New("existing generated state uses a different mode")
		}
		return claimEnvironmentID(options.Registry, options.EnvironmentID, options.Output)
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("inspect existing generated state")
	}
	if err := prepareEmptyPrivateDirectory(options.Output); err != nil {
		return err
	}
	if err := claimEnvironmentID(options.Registry, options.EnvironmentID, options.Output); err != nil {
		return err
	}
	materials, fingerprint, err := generateComposeMaterials(options, lookup)
	if err != nil {
		return err
	}
	for _, name := range stableComposeFiles {
		value, ok := materials[name]
		if !ok {
			return fmt.Errorf("material %s was not generated", name)
		}
		if err := atomicPrivateWrite(filepath.Join(options.Output, name), value); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}
	manifest := generatedManifest{SchemaVersion: 1, EnvironmentID: options.EnvironmentID, Mode: options.Mode, CreatedAt: options.Now.UTC(), GatewayFingerprint: fingerprint, Files: map[string]generatedFile{}, EphemeralFiles: []string{"agent-enrollment-token", "gateway-enrollment-token"}}
	for _, name := range stableComposeFiles {
		value := materials[name]
		digest := sha256.Sum256(value)
		public := strings.HasSuffix(name, "-cert.pem") || name == "platform-ca.pem" || name == "route-verifying-key" || name == "gateway-certificate-fingerprint"
		manifest.Files[name] = generatedFile{SHA256: hex.EncodeToString(digest[:]), Size: int64(len(value)), Sensitive: !public}
	}
	encoded, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return errors.New("encode generated-state manifest")
	}
	encoded = append(encoded, '\n')
	if err := atomicPrivateWrite(filepath.Join(options.Output, manifestName), encoded); err != nil {
		return err
	}
	_, err = validateGeneratedState(options.Output, options.EnvironmentID)
	return err
}

func generateComposeMaterials(options materializeOptions, lookup func(string) (string, bool)) (map[string][]byte, string, error) {
	random := func(size int) ([]byte, error) {
		value := make([]byte, size)
		_, err := io.ReadFull(rand.Reader, value)
		return value, err
	}
	postgresPassword, err := random(32)
	if err != nil {
		return nil, "", errors.New("generate PostgreSQL password")
	}
	normalPassword, err := random(32)
	if err != nil {
		return nil, "", errors.New("generate normal database password")
	}
	platformPassword, err := random(32)
	if err != nil {
		return nil, "", errors.New("generate platform database password")
	}
	redisPassword, err := random(32)
	if err != nil {
		return nil, "", errors.New("generate Redis password")
	}
	encode := func(value []byte) []byte { return []byte(base64.RawURLEncoding.EncodeToString(value) + "\n") }
	materials := map[string][]byte{
		"postgres-password": encode(postgresPassword), "database-normal-password": encode(normalPassword), "database-platform-password": encode(platformPassword),
		"redis-password": encode(redisPassword),
	}
	oidcSecret, err := random(32)
	if err != nil {
		return nil, "", errors.New("generate OIDC client secret")
	}
	keyring, err := random(32)
	if err != nil {
		return nil, "", errors.New("generate control-plane keyring")
	}
	keycloakPassword, err := random(32)
	if err != nil {
		return nil, "", errors.New("generate Keycloak bootstrap password")
	}
	materials["oidc-client-secret"] = encode(oidcSecret)
	materials["control-plane-keyring"] = keyring
	materials["keycloak-bootstrap-password"] = encode(keycloakPassword)
	redisText := strings.TrimSpace(string(materials["redis-password"]))
	materials["redis-acl"] = []byte("user default off\nuser scheduler on >" + redisText + " ~* +@all\n")
	if options.Mode == "external" {
		for target, env := range map[string]string{"database-normal-dsn": "AJIASU_DATABASE_NORMAL_DSN_FILE", "database-platform-dsn": "AJIASU_DATABASE_PLATFORM_DSN_FILE", "database-migration-dsn": "AJIASU_DATABASE_MIGRATION_DSN_FILE"} {
			value, readErr := readRequiredSecretFile(lookup, env)
			if readErr != nil {
				return nil, "", readErr
			}
			materials[target] = value
		}
	} else {
		escape := func(value []byte) string { return base64.RawURLEncoding.EncodeToString(value) }
		materials["database-normal-dsn"] = []byte("postgresql://ajiasu_normal:" + escape(normalPassword) + "@postgres:5432/ajiasu?sslmode=disable\n")
		materials["database-platform-dsn"] = []byte("postgresql://ajiasu_control:" + escape(platformPassword) + "@postgres:5432/ajiasu?sslmode=disable\n")
		materials["database-migration-dsn"] = []byte("postgresql://ajiasu_admin:" + escape(postgresPassword) + "@postgres:5432/ajiasu?sslmode=disable\n")
	}
	seed, err := random(ed25519.SeedSize)
	if err != nil {
		return nil, "", errors.New("generate route signing key")
	}
	materials["route-signing-key"] = seed
	materials["route-verifying-key"] = ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey)
	certificates, fingerprint, err := generateCertificates(options.EnvironmentID, options.Now)
	if err != nil {
		return nil, "", err
	}
	for name, value := range certificates {
		materials[name] = value
	}
	materials["gateway-certificate-fingerprint"] = []byte(fingerprint + "\n")
	return materials, fingerprint, nil
}

func generateCertificates(environmentID string, now time.Time) (map[string][]byte, string, error) {
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, "", errors.New("generate platform CA key")
	}
	serial, err := randomSerial()
	if err != nil {
		return nil, "", err
	}
	caTemplate := x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "AJiaSu " + environmentID + " platform CA"}, NotBefore: now.Add(-5 * time.Minute), NotAfter: now.AddDate(10, 0, 0), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign | x509.KeyUsageDigitalSignature}
	caDER, err := x509.CreateCertificate(rand.Reader, &caTemplate, &caTemplate, &caKey.PublicKey, caKey)
	if err != nil {
		return nil, "", errors.New("create platform CA")
	}
	caPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER})
	result := map[string][]byte{"platform-ca.pem": caPEM}
	type leaf struct {
		name, commonName string
		dns              []string
		usages           []x509.ExtKeyUsage
	}
	leaves := []leaf{
		{name: "control-plane", commonName: "control-plane", dns: []string{"control-plane", "localhost"}, usages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}},
		{name: "agent", commonName: "agent-" + environmentID, usages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}},
		{name: "agent-relay", commonName: "agent", dns: []string{"agent", "localhost"}, usages: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}},
		{name: "gateway", commonName: "gateway-" + environmentID, usages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}},
	}
	fingerprint := ""
	for _, item := range leaves {
		key, keyErr := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if keyErr != nil {
			return nil, "", errors.New("generate TLS key")
		}
		leafSerial, serialErr := randomSerial()
		if serialErr != nil {
			return nil, "", serialErr
		}
		template := x509.Certificate{SerialNumber: leafSerial, Subject: pkix.Name{CommonName: item.commonName}, DNSNames: item.dns, NotBefore: now.Add(-5 * time.Minute), NotAfter: now.AddDate(1, 0, 0), BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: item.usages}
		if item.name == "control-plane" || item.name == "agent-relay" {
			template.IPAddresses = []net.IP{net.ParseIP("127.0.0.1")}
		}
		der, createErr := x509.CreateCertificate(rand.Reader, &template, &caTemplate, &key.PublicKey, caKey)
		if createErr != nil {
			return nil, "", errors.New("create TLS certificate")
		}
		keyDER, marshalErr := x509.MarshalPKCS8PrivateKey(key)
		if marshalErr != nil {
			return nil, "", errors.New("marshal TLS key")
		}
		result[item.name+"-cert.pem"] = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
		result[item.name+"-key.pem"] = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
		if item.name == "gateway" {
			digest := sha256.Sum256(der)
			fingerprint = hex.EncodeToString(digest[:])
		}
	}
	return result, fingerprint, nil
}

func randomSerial() (*big.Int, error) {
	limit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, limit)
	if err != nil {
		return nil, errors.New("generate certificate serial")
	}
	return serial, nil
}

func validateGeneratedState(directory, environmentID string) (generatedManifest, error) {
	var manifest generatedManifest
	if directory == "" || !validEnvironmentID(environmentID) {
		return manifest, errors.New("output and environment ID are required")
	}
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return manifest, errors.New("generated-state directory is unavailable or unsafe")
	}
	if err := requirePrivateMode(info, true); err != nil {
		return manifest, err
	}
	encoded, err := readRegularPrivateFile(filepath.Join(directory, manifestName), 1<<20)
	if err != nil {
		return manifest, err
	}
	decoder := json.NewDecoder(strings.NewReader(string(encoded)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&manifest); err != nil || manifest.SchemaVersion != 1 || manifest.EnvironmentID != environmentID {
		return manifest, errors.New("generated-state manifest is invalid or belongs to another environment")
	}
	allowed := map[string]bool{manifestName: true}
	for _, name := range stableComposeFiles {
		allowed[name] = true
	}
	for name := range ephemeralComposeFiles {
		allowed[name] = true
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return manifest, errors.New("list generated state")
	}
	for _, entry := range entries {
		if !allowed[entry.Name()] {
			return manifest, fmt.Errorf("unexpected generated-state entry %s", entry.Name())
		}
		if entry.Type()&os.ModeSymlink != 0 || entry.IsDir() {
			return manifest, fmt.Errorf("unsafe generated-state entry %s", entry.Name())
		}
	}
	for _, name := range stableComposeFiles {
		expected, ok := manifest.Files[name]
		if !ok {
			return manifest, fmt.Errorf("manifest omits %s", name)
		}
		value, readErr := readRegularPrivateFile(filepath.Join(directory, name), 1<<20)
		if readErr != nil {
			return manifest, fmt.Errorf("validate %s: %w", name, readErr)
		}
		digest := sha256.Sum256(value)
		if expected.Size != int64(len(value)) || expected.SHA256 != hex.EncodeToString(digest[:]) {
			return manifest, fmt.Errorf("generated-state file %s does not match its manifest", name)
		}
	}
	for name := range ephemeralComposeFiles {
		path := filepath.Join(directory, name)
		if _, statErr := os.Lstat(path); statErr == nil {
			if _, readErr := readRegularPrivateFile(path, 64*1024); readErr != nil {
				return manifest, readErr
			}
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return manifest, statErr
		}
	}
	return manifest, nil
}

func prepareEmptyPrivateDirectory(path string) error {
	if info, err := os.Lstat(path); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("generated-state path is not a safe directory")
		}
		entries, readErr := os.ReadDir(path)
		if readErr != nil {
			return errors.New("inspect generated-state directory")
		}
		if len(entries) != 0 {
			return errors.New("generated-state directory contains unexpected existing files")
		}
		return requirePrivateMode(info, true)
	} else if !errors.Is(err, os.ErrNotExist) {
		return errors.New("inspect generated-state path")
	}
	if err := os.MkdirAll(path, 0o700); err != nil {
		return errors.New("create generated-state directory")
	}
	return os.Chmod(path, 0o700)
}

func atomicPrivateWrite(path string, value []byte) error {
	if _, err := os.Lstat(path); err == nil {
		return os.ErrExist
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".partial-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(value); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Link(temporaryPath, path); err != nil {
		return err
	}
	return os.Chmod(path, 0o600)
}

func readRegularPrivateFile(path string, limit int64) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil || !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("required private file is unavailable or unsafe")
	}
	if err := requirePrivateMode(before, false); err != nil {
		return nil, err
	}
	if before.Size() < 1 || before.Size() > limit {
		return nil, errors.New("private file size is invalid")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, errors.New("open private file")
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil || !after.Mode().IsRegular() || !os.SameFile(before, after) {
		return nil, errors.New("private file changed while opening")
	}
	value, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil || int64(len(value)) < 1 || int64(len(value)) > limit {
		return nil, errors.New("read private file")
	}
	return value, nil
}

func requirePrivateMode(info os.FileInfo, directory bool) error {
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return errors.New("generated state has group or world permissions")
	}
	if directory && !info.IsDir() {
		return errors.New("private path is not a directory")
	}
	return nil
}

func claimEnvironmentID(registry, environmentID, output string) error {
	if strings.TrimSpace(registry) == "" {
		return nil
	}
	if err := os.MkdirAll(registry, 0o700); err != nil {
		return errors.New("create environment registry")
	}
	resolved, err := filepath.Abs(output)
	if err != nil {
		return errors.New("resolve generated-state path")
	}
	marker := filepath.Join(registry, environmentID+".json")
	if value, readErr := os.ReadFile(marker); readErr == nil {
		var existing struct {
			Output string `json:"output"`
		}
		if json.Unmarshal(value, &existing) != nil || !strings.EqualFold(filepath.Clean(existing.Output), filepath.Clean(resolved)) {
			return errors.New("environment ID is already claimed by another generated-state directory")
		}
		return nil
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return errors.New("inspect environment registry")
	}
	encoded, _ := json.Marshal(struct {
		Output string `json:"output"`
	}{Output: resolved})
	return atomicPrivateWrite(marker, append(encoded, '\n'))
}

func validEnvironmentID(value string) bool {
	if len(value) < 1 || len(value) > 63 {
		return false
	}
	for i, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || (r == '-' && i > 0 && i < len(value)-1) {
			continue
		}
		return false
	}
	return true
}

func readRequiredSecretFile(lookup func(string) (string, bool), name string) ([]byte, error) {
	path, ok := lookup(name)
	if !ok || strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("%s is required", name)
	}
	value, err := readRegularPrivateFile(path, 64*1024)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", name, err)
	}
	if strings.TrimSpace(string(value)) == "" {
		return nil, fmt.Errorf("%s is empty", name)
	}
	return value, nil
}

func executeComposeAgentEnrollment(ctx context.Context, args []string, lookup func(string) (string, bool), stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("compose enroll-agent", flag.ContinueOnError)
	fs.SetOutput(stderr)
	name := fs.String("name", "", "expected Agent name")
	output := fs.String("output", "", "token output file")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || strings.TrimSpace(*name) == "" || *output == "" {
		return errors.New("name and output are required")
	}
	return withComposePools(ctx, lookup, func(pools *database.Pools) error {
		actor, err := composePlatformActor()
		if err != nil {
			return err
		}
		service, err := nodes.NewService(pools, audit.NewService())
		if err != nil {
			return errors.New("create node enrollment service")
		}
		created, err := service.CreateEnrollment(ctx, actor, nodes.CreateEnrollment{ExpectedNodeName: *name, ValidFor: time.Hour})
		if err != nil {
			return errors.New("create Agent enrollment")
		}
		return writeEnrollmentToken(*output, created.Token, stdout)
	})
}

func executeComposeGatewayEnrollment(ctx context.Context, args []string, lookup func(string) (string, bool), stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("compose enroll-gateway", flag.ContinueOnError)
	fs.SetOutput(stderr)
	name := fs.String("name", "", "expected Gateway name")
	fingerprintFile := fs.String("fingerprint-file", "", "fingerprint input file")
	output := fs.String("output", "", "token output file")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 || strings.TrimSpace(*name) == "" || *fingerprintFile == "" || *output == "" {
		return errors.New("name, fingerprint-file, and output are required")
	}
	fingerprintValue, err := readRegularPrivateFile(*fingerprintFile, 1024)
	if err != nil {
		return errors.New("read Gateway fingerprint")
	}
	fingerprint := strings.TrimSpace(string(fingerprintValue))
	return withComposePools(ctx, lookup, func(pools *database.Pools) error {
		service, err := gateways.NewService(pools)
		if err != nil {
			return errors.New("create Gateway enrollment service")
		}
		_, token, err := service.CreateEnrollment(ctx, *name, fingerprint, uuid.New(), time.Hour)
		if err != nil {
			return errors.New("create Gateway enrollment")
		}
		return writeEnrollmentToken(*output, token, stdout)
	})
}

func writeEnrollmentToken(output, token string, stdout io.Writer) error {
	if output == "-" {
		_, err := fmt.Fprintln(stdout, token)
		return err
	}
	return atomicPrivateWrite(output, []byte(token+"\n"))
}

func withComposePools(ctx context.Context, lookup func(string) (string, bool), operation func(*database.Pools) error) error {
	cfg, err := config.Load(lookup)
	if err != nil {
		return errors.New("configuration is invalid")
	}
	pools, err := database.OpenPools(ctx, cfg.Database)
	if err != nil {
		return errors.New("database is unavailable")
	}
	defer pools.Close()
	return operation(pools)
}

func composePlatformActor() (tenancy.PlatformActor, error) {
	return tenancy.NewPlatformActor(tenancy.Subject{ActorID: uuid.New(), PlatformRoles: []tenancy.Role{tenancy.PlatformAdmin}}, tenancy.ActorMetadata{ActorType: "compose-bootstrap", SourceIP: netip.MustParseAddr("127.0.0.1"), UserAgent: "control-plane-compose/1.0", RequestID: uuid.New()})
}
