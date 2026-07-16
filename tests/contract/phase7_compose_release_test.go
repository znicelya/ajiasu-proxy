package contract_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

const phase7Revision1SHA256 = "db9865b85dba1254fe4a6f1bd683c679fca51c38adc15978f6f09426d8e97bc6"

type configurationMatrix struct {
	Revision        int                   `yaml:"revision"`
	DeploymentModes []string              `yaml:"deployment_modes"`
	Owners          []string              `yaml:"owners"`
	Configurations  []configurationRecord `yaml:"configurations"`
}

type configurationRecord struct {
	Name                 string   `yaml:"name"`
	Owner                string   `yaml:"owner"`
	ContainerEnvironment *string  `yaml:"container_environment"`
	SecretMount          *string  `yaml:"secret_mount"`
	DefaultPolicy        string   `yaml:"default_policy"`
	Modes                []string `yaml:"modes"`
	HelmValue            string   `yaml:"helm_value"`
}

func TestPhase7ReleaseManifestRevision1(t *testing.T) {
	root := phase7RepositoryRoot(t)
	fixturePath := filepath.Join(root, "deploy", "compose", "testdata", "revision-1.json")
	fixtureBytes := readPhase7File(t, fixturePath)
	normalized := bytes.ReplaceAll(fixtureBytes, []byte("\r\n"), []byte("\n"))
	digest := sha256.Sum256(normalized)
	if got := hex.EncodeToString(digest[:]); got != phase7Revision1SHA256 {
		t.Fatalf("revision-1 fixture changed: sha256=%s", got)
	}

	manifest := decodeJSONObject(t, fixtureBytes)
	schema := compilePhase7ReleaseSchema(t, root)
	if err := schema.Validate(manifest); err != nil {
		t.Fatalf("revision-1 fixture does not satisfy schema: %v", err)
	}
	if err := validateReleaseSemantics(manifest); err != nil {
		t.Fatalf("revision-1 fixture violates release semantics: %v", err)
	}

	topology := manifest["topology"].(map[string]any)
	assertStringArray(t, topology["services"], []string{"migration", "control-plane", "gateway", "agent"})
	assertStringArray(t, topology["networks"], []string{"edge", "control", "dependencies"})
	assertStringArray(t, topology["volumes"], []string{"postgres-data", "agent-state", "gateway-state"})
	assertStringArray(t, topology["health_outcomes"], []string{"live", "ready", "not_ready", "degraded", "draining", "stopped"})

	console := manifest["profiles"].(map[string]any)["console"].(map[string]any)
	if console["state"] != "reserved" || console["image"] != nil {
		t.Fatalf("console profile must remain reserved without an image: %#v", console)
	}
}

func TestPhase7ReleaseManifestRejectsInvalidContracts(t *testing.T) {
	root := phase7RepositoryRoot(t)
	fixture := decodeJSONObject(t, readPhase7File(t, filepath.Join(root, "deploy", "compose", "testdata", "revision-1.json")))
	schema := compilePhase7ReleaseSchema(t, root)

	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{
			name: "unknown manifest revision",
			mutate: func(document map[string]any) {
				document["manifest_revision"] = float64(2)
			},
		},
		{
			name: "missing image digest",
			mutate: func(document map[string]any) {
				delete(document["images"].(map[string]any)["gateway"].(map[string]any), "digest")
			},
		},
		{
			name: "mutable image tag",
			mutate: func(document map[string]any) {
				document["images"].(map[string]any)["agent"].(map[string]any)["repository"] = "ghcr.io/dnomd343/ajiasu-agent:latest"
			},
		},
		{
			name: "unsupported architecture",
			mutate: func(document map[string]any) {
				document["compatibility"].(map[string]any)["platforms"] = []any{"linux/amd64", "linux/s390x"}
			},
		},
		{
			name: "implemented console image",
			mutate: func(document map[string]any) {
				document["profiles"].(map[string]any)["console"].(map[string]any)["image"] = "ghcr.io/dnomd343/ajiasu-console@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := cloneJSONObject(t, fixture)
			test.mutate(candidate)
			if err := schema.Validate(candidate); err == nil {
				t.Fatal("invalid release manifest was accepted")
			}
		})
	}
}

func TestPhase7ReleaseManifestRejectsDuplicatePublishedPorts(t *testing.T) {
	root := phase7RepositoryRoot(t)
	manifest := decodeJSONObject(t, readPhase7File(t, filepath.Join(root, "deploy", "compose", "testdata", "revision-1.json")))
	ports := manifest["topology"].(map[string]any)["published_ports"].([]any)
	ports[1].(map[string]any)["host_bind"] = ports[0].(map[string]any)["host_bind"]
	ports[1].(map[string]any)["host_port"] = ports[0].(map[string]any)["host_port"]
	ports[1].(map[string]any)["protocol"] = ports[0].(map[string]any)["protocol"]
	if err := validateReleaseSemantics(manifest); err == nil || !strings.Contains(err.Error(), "duplicate published port") {
		t.Fatalf("duplicate published port was not rejected: %v", err)
	}
}

func TestPhase7ConfigurationMatrixGoldenContract(t *testing.T) {
	root := phase7RepositoryRoot(t)
	matrix := readConfigurationMatrix(t, filepath.Join(root, "deploy", "compose", "configuration-matrix.yaml"))
	if err := validateConfigurationMatrix(matrix); err != nil {
		t.Fatal(err)
	}

	wantOwners := map[string]string{
		"AJIASU_RELEASE_MANIFEST":               "release",
		"AJIASU_ENVIRONMENT_ID":                 "lifecycle",
		"AJIASU_HTTP_BIND":                      "control-plane",
		"AJIASU_AGENT_GRPC_BIND":                "control-plane",
		"AJIASU_GATEWAY_GRPC_BIND":              "control-plane",
		"AJIASU_DATABASE_NORMAL_DSN_FILE":       "control-plane",
		"AJIASU_DATABASE_PLATFORM_DSN_FILE":     "control-plane",
		"AJIASU_REDIS_PASSWORD_FILE":            "control-plane",
		"AJIASU_OIDC_CLIENT_SECRET_FILE":        "control-plane",
		"AJIASU_KEYRING_FILE":                   "control-plane",
		"AJIASU_GATEWAY_HTTP_LISTEN":            "gateway",
		"AJIASU_GATEWAY_SOCKS5_LISTEN":          "gateway",
		"AJIASU_GATEWAY_CONTROL_PLANE_ENDPOINT": "gateway",
		"AJIASU_GATEWAY_STATE_DIRECTORY":        "gateway",
		"AJIASU_GATEWAY_ENROLLMENT_TOKEN_FILE":  "gateway",
		"AJIASU_AGENT_CONTROL_PLANE_ENDPOINT":   "agent",
		"AJIASU_AGENT_STATE_DIRECTORY":          "agent",
		"AJIASU_AGENT_ENROLLMENT_TOKEN_FILE":    "agent",
		"AJIASU_AGENT_RUNNER_IMAGE":             "agent",
		"AJIASU_DOCKER_SOCKET":                  "agent",
		"AJIASU_DOCKER_GID":                     "agent",
		"AJIASU_SCHEDULER_LEASE_NAMESPACE":      "control-plane",
	}
	if len(matrix.Configurations) != len(wantOwners) {
		t.Fatalf("configuration count=%d, want %d", len(matrix.Configurations), len(wantOwners))
	}
	for _, record := range matrix.Configurations {
		if want := wantOwners[record.Name]; want == "" || record.Owner != want {
			t.Errorf("configuration %s owner=%q, want %q", record.Name, record.Owner, want)
		}
	}

	wantModes := []string{"development", "single-host", "external-dependencies"}
	if fmt.Sprint(matrix.DeploymentModes) != fmt.Sprint(wantModes) {
		t.Fatalf("deployment modes=%v, want %v", matrix.DeploymentModes, wantModes)
	}

	manifest := decodeJSONObject(t, readPhase7File(t, filepath.Join(root, "deploy", "compose", "testdata", "revision-1.json")))
	manifestMounts := stringSet(manifest["topology"].(map[string]any)["secret_mounts"].([]any))
	matrixMounts := make(map[string]struct{})
	for _, record := range matrix.Configurations {
		if record.SecretMount != nil {
			matrixMounts[*record.SecretMount] = struct{}{}
		}
	}
	if fmt.Sprint(sortedKeys(matrixMounts)) != fmt.Sprint(sortedKeys(manifestMounts)) {
		t.Fatalf("matrix secret mounts=%v, manifest secret mounts=%v", sortedKeys(matrixMounts), sortedKeys(manifestMounts))
	}
}

func TestPhase7ConfigurationMatrixRejectsMissingOwner(t *testing.T) {
	root := phase7RepositoryRoot(t)
	matrix := readConfigurationMatrix(t, filepath.Join(root, "deploy", "compose", "configuration-matrix.yaml"))
	matrix.Configurations[0].Owner = ""
	if err := validateConfigurationMatrix(matrix); err == nil || !strings.Contains(err.Error(), "owner") {
		t.Fatalf("configuration without owner was not rejected: %v", err)
	}
}

func TestPhase7CompatibilityMatrixContract(t *testing.T) {
	root := phase7RepositoryRoot(t)
	content := string(readPhase7File(t, filepath.Join(root, "docs", "operations", "compatibility-matrix.md")))
	for _, required := range []string{
		"| Control Plane schema | 11 |",
		"| Agent control protocol | revision 2 | revision 1 |",
		"| Gateway control protocol | revision 1 | revision 1 |",
		"| Relay protocol | revision 1 | revision 1 |",
		"| Scheduler protocol | revision 1 | No previous revision |",
		"| Compose release manifest | revision 1 | No previous revision |",
		"| Compose configuration matrix | revision 1 | No previous revision |",
		"| Container host OS | Linux kernel 5.15 or newer |",
		"| Host architecture | `linux/amd64`, `linux/arm64` |",
		"| Docker Engine | 27.x or newer |",
		"| Docker Compose | v2.33.1 or newer |",
		"| Docker Buildx | v0.19 or newer |",
		"one active Gateway for exact totals",
	} {
		if !strings.Contains(content, required) {
			t.Errorf("compatibility matrix is missing %q", required)
		}
	}
}

func TestPhase7CommittedContractsContainNoSecretValuesOrDefaults(t *testing.T) {
	root := phase7RepositoryRoot(t)
	matrixPath := filepath.Join(root, "deploy", "compose", "configuration-matrix.yaml")
	matrix := readConfigurationMatrix(t, matrixPath)
	for _, record := range matrix.Configurations {
		upperName := strings.ToUpper(record.Name)
		secretBearing := strings.Contains(upperName, "PASSWORD") ||
			strings.Contains(upperName, "TOKEN") ||
			strings.Contains(upperName, "SECRET") ||
			strings.Contains(upperName, "KEYRING") ||
			strings.Contains(upperName, "DSN")
		if !secretBearing {
			continue
		}
		if !strings.HasSuffix(record.Name, "_FILE") || record.SecretMount == nil {
			t.Errorf("secret-bearing configuration %s is not file-backed", record.Name)
		}
		if !strings.Contains(record.DefaultPolicy, "secret_file") {
			t.Errorf("secret-bearing configuration %s has unsafe default policy %q", record.Name, record.DefaultPolicy)
		}
	}

	for _, relative := range []string{
		"docs/adr/0005-compose-runtime-boundary.md",
		"deploy/compose/release-manifest.schema.json",
		"deploy/compose/configuration-matrix.yaml",
		"deploy/compose/testdata/revision-1.json",
	} {
		content := string(readPhase7File(t, filepath.Join(root, filepath.FromSlash(relative))))
		for _, forbidden := range []*regexp.Regexp{
			regexp.MustCompile(`(?i)postgres(?:ql)?://[^\s:/]+:[^\s@]+@`),
			regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----`),
			regexp.MustCompile(`(?i)(password|enrollment[_-]?token|client[_-]?secret)\s*[:=]\s*["']?[A-Za-z0-9+/=_-]{16,}`),
		} {
			if forbidden.MatchString(content) {
				t.Errorf("%s contains a committed secret-like value matching %s", relative, forbidden)
			}
		}
	}
}

func validateReleaseSemantics(manifest map[string]any) error {
	topology, ok := manifest["topology"].(map[string]any)
	if !ok {
		return fmt.Errorf("topology is missing")
	}
	ports, ok := topology["published_ports"].([]any)
	if !ok {
		return fmt.Errorf("published ports are missing")
	}
	seenPorts := make(map[string]struct{}, len(ports))
	seenNames := make(map[string]struct{}, len(ports))
	for _, raw := range ports {
		port, ok := raw.(map[string]any)
		if !ok {
			return fmt.Errorf("published port is not an object")
		}
		name, _ := port["name"].(string)
		if _, duplicate := seenNames[name]; duplicate {
			return fmt.Errorf("duplicate published port name %q", name)
		}
		seenNames[name] = struct{}{}
		key := fmt.Sprintf("%s:%v/%s", port["host_bind"], port["host_port"], port["protocol"])
		if _, duplicate := seenPorts[key]; duplicate {
			return fmt.Errorf("duplicate published port %s", key)
		}
		seenPorts[key] = struct{}{}
	}
	return nil
}

func validateConfigurationMatrix(matrix configurationMatrix) error {
	if matrix.Revision != 1 {
		return fmt.Errorf("unsupported configuration matrix revision %d", matrix.Revision)
	}
	allowedOwners := make(map[string]struct{}, len(matrix.Owners))
	for _, owner := range matrix.Owners {
		if owner == "" {
			return fmt.Errorf("configuration owner registry contains an empty owner")
		}
		if _, duplicate := allowedOwners[owner]; duplicate {
			return fmt.Errorf("duplicate configuration owner %q", owner)
		}
		allowedOwners[owner] = struct{}{}
	}
	allowedModes := make(map[string]struct{}, len(matrix.DeploymentModes))
	for _, mode := range matrix.DeploymentModes {
		allowedModes[mode] = struct{}{}
	}
	seenNames := make(map[string]struct{}, len(matrix.Configurations))
	seenHelmValues := make(map[string]struct{}, len(matrix.Configurations))
	for _, record := range matrix.Configurations {
		if record.Name == "" {
			return fmt.Errorf("configuration name is empty")
		}
		if _, duplicate := seenNames[record.Name]; duplicate {
			return fmt.Errorf("duplicate configuration name %q", record.Name)
		}
		seenNames[record.Name] = struct{}{}
		if _, known := allowedOwners[record.Owner]; !known {
			return fmt.Errorf("configuration %s has unknown or empty owner %q", record.Name, record.Owner)
		}
		if record.DefaultPolicy == "" {
			return fmt.Errorf("configuration %s has no default policy", record.Name)
		}
		if record.HelmValue == "" {
			return fmt.Errorf("configuration %s has no future Helm mapping", record.Name)
		}
		if _, duplicate := seenHelmValues[record.HelmValue]; duplicate {
			return fmt.Errorf("duplicate future Helm mapping %q", record.HelmValue)
		}
		seenHelmValues[record.HelmValue] = struct{}{}
		if len(record.Modes) == 0 {
			return fmt.Errorf("configuration %s has no deployment mode", record.Name)
		}
		for _, mode := range record.Modes {
			if _, known := allowedModes[mode]; !known {
				return fmt.Errorf("configuration %s uses unknown mode %q", record.Name, mode)
			}
		}
		if record.SecretMount != nil && !strings.HasPrefix(*record.SecretMount, "/run/secrets/ajiasu/") {
			return fmt.Errorf("configuration %s has invalid secret mount", record.Name)
		}
	}
	return nil
}

func compilePhase7ReleaseSchema(t *testing.T, root string) *jsonschema.Schema {
	t.Helper()
	schemaBytes := readPhase7File(t, filepath.Join(root, "deploy", "compose", "release-manifest.schema.json"))
	var schemaDocument any
	if err := json.Unmarshal(schemaBytes, &schemaDocument); err != nil {
		t.Fatal(err)
	}
	compiler := jsonschema.NewCompiler()
	if err := compiler.AddResource("release-manifest.schema.json", schemaDocument); err != nil {
		t.Fatal(err)
	}
	schema, err := compiler.Compile("release-manifest.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

func readConfigurationMatrix(t *testing.T, path string) configurationMatrix {
	t.Helper()
	content := readPhase7File(t, path)
	decoder := yaml.NewDecoder(bytes.NewReader(content))
	decoder.KnownFields(true)
	var matrix configurationMatrix
	if err := decoder.Decode(&matrix); err != nil {
		t.Fatal(err)
	}
	return matrix
}

func phase7RepositoryRoot(t *testing.T) string {
	t.Helper()
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test source")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
}

func readPhase7File(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func decodeJSONObject(t *testing.T, content []byte) map[string]any {
	t.Helper()
	var document map[string]any
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	if err := decoder.Decode(&document); err != nil {
		t.Fatal(err)
	}
	return document
}

func cloneJSONObject(t *testing.T, document map[string]any) map[string]any {
	t.Helper()
	content, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return decodeJSONObject(t, content)
}

func assertStringArray(t *testing.T, raw any, want []string) {
	t.Helper()
	items, ok := raw.([]any)
	if !ok {
		t.Fatalf("value %#v is not an array", raw)
	}
	got := make([]string, len(items))
	for index, item := range items {
		got[index], ok = item.(string)
		if !ok {
			t.Fatalf("array item %#v is not a string", item)
		}
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Fatalf("array=%v, want %v", got, want)
	}
}

func stringSet(items []any) map[string]struct{} {
	set := make(map[string]struct{}, len(items))
	for _, item := range items {
		set[item.(string)] = struct{}{}
	}
	return set
}

func sortedKeys(set map[string]struct{}) []string {
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
