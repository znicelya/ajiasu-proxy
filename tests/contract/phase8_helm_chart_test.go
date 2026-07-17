package contract_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPhase8HelmChartSecurityContracts(t *testing.T) {
	root := phase7RepositoryRoot(t)
	chart := filepath.Join(root, "deploy", "helm", "ajiasu")

	required := []string{
		"Chart.yaml", "values.yaml", "values.schema.json",
		filepath.Join("templates", "control-plane.yaml"),
		filepath.Join("templates", "gateway.yaml"),
		filepath.Join("templates", "agent.yaml"),
		filepath.Join("templates", "migration-job.yaml"),
		filepath.Join("templates", "runner-pod-template.yaml"),
		filepath.Join("templates", "networkpolicy.yaml"),
		filepath.Join("templates", "rbac.yaml"),
	}
	for _, name := range required {
		if _, err := os.Stat(filepath.Join(chart, name)); err != nil {
			t.Fatalf("required Phase 8 chart file %s: %v", name, err)
		}
	}

	values := string(readPhase7File(t, filepath.Join(chart, "values.yaml")))
	for _, forbidden := range []string{"password:", "clientSecret:", "privateKey:"} {
		if strings.Contains(values, forbidden) {
			t.Fatalf("values.yaml must not accept literal secret field %q", forbidden)
		}
	}

	runner := string(readPhase7File(t, filepath.Join(chart, "templates", "runner-pod-template.yaml")))
	for _, requiredText := range []string{
		"automountServiceAccountToken: false",
		"readOnlyRootFilesystem: true",
		"runAsUser: 65532",
		"capabilities: { drop: [ALL] }",
	} {
		if !strings.Contains(runner, requiredText) {
			t.Fatalf("runner template missing security contract %q", requiredText)
		}
	}

	agent := string(readPhase7File(t, filepath.Join(chart, "templates", "agent.yaml")))
	if !strings.Contains(agent, "hostPath:") || !strings.Contains(agent, "runtimeSocket") {
		t.Fatal("only the Agent template must own the runtime socket contract")
	}
	for _, name := range []string{"control-plane.yaml", "gateway.yaml", "migration-job.yaml"} {
		body := string(readPhase7File(t, filepath.Join(chart, "templates", name)))
		if strings.Contains(body, "hostPath:") {
			t.Fatalf("%s must not mount a host path", name)
		}
	}
	migration := string(readPhase7File(t, filepath.Join(chart, "templates", "migration-job.yaml")))
	if !strings.Contains(migration, "args: [\"migrate\", \"up\"]") {
		t.Fatal("migration hook must invoke the explicit migrate up command")
	}
	for _, name := range []string{"helm-preflight.ps1", "helm-install.ps1", "helm-drain.ps1", "helm-rollback.ps1"} {
		if _, err := os.Stat(filepath.Join(root, "scripts", name)); err != nil {
			t.Fatalf("required Phase 8 operator script %s: %v", name, err)
		}
	}
}

func TestPhase8ValuesSchemaRequiresImmutableDigests(t *testing.T) {
	root := phase7RepositoryRoot(t)
	data := readPhase7File(t, filepath.Join(root, "deploy", "helm", "ajiasu", "values.schema.json"))
	var schema map[string]any
	if err := json.Unmarshal(data, &schema); err != nil {
		t.Fatalf("decode Phase 8 values schema: %v", err)
	}
	defs := schema["$defs"].(map[string]any)
	image := defs["image"].(map[string]any)
	properties := image["properties"].(map[string]any)
	digest := properties["digest"].(map[string]any)
	if digest["pattern"] != "^sha256:[0-9a-f]{64}$" {
		t.Fatalf("unexpected immutable digest contract: %#v", digest["pattern"])
	}
}

func TestPhase8CompatibilityAndReleaseExamples(t *testing.T) {
	root := phase7RepositoryRoot(t)
	compatibility := string(readPhase7File(t, filepath.Join(root, "deploy", "helm", "ajiasu", "compatibility.yaml")))
	for _, required := range []string{"schema_version: 11", "migration_hook_required: true", "redis_loss_blocks_new_pool_allocations: true", "rollback_requires_backup: true"} {
		if !strings.Contains(compatibility, required) {
			t.Fatalf("compatibility contract missing %q", required)
		}
	}
	release := string(readPhase7File(t, filepath.Join(root, "testdata", "phase8-release-manifest.example.yaml")))
	if strings.Contains(release, ":latest") || !strings.Contains(release, "@sha256:") {
		t.Fatal("Phase 8 release example must use immutable image references")
	}
}
