package contract_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

func TestPhase7ComposeImageLock(t *testing.T) {
	root := phase7RepositoryRoot(t)
	content := strings.ReplaceAll(string(readPhase7File(t, filepath.Join(root, "build", "compose-images.lock"))), "\r\n", "\n")
	lines := strings.Split(strings.TrimSpace(content), "\n")
	want := []string{"GO_BUILD_IMAGE", "KEYCLOAK_IMAGE", "POSTGRES_IMAGE", "REDIS_IMAGE", "RUNTIME_IMAGE", "RUST_BUILD_IMAGE", "SBOM_SCANNER_IMAGE"}
	got := make([]string, 0, len(lines))
	pattern := regexp.MustCompile(`^([A-Z_]+)=([^\s:@]+(?:/[^\s:@]+)*:[^\s@]+)@sha256:([0-9a-f]{64})$`)
	for _, line := range lines {
		match := pattern.FindStringSubmatch(line)
		if match == nil || strings.Contains(line, ":latest") || match[3] == strings.Repeat("0", 64) {
			t.Fatalf("invalid locked image %q", line)
		}
		got = append(got, match[1])
	}
	sort.Strings(got)
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("locked image keys=%v, want %v", got, want)
	}
}

func TestPhase7ReleaseDockerfilesAreHardenedAndMultiArch(t *testing.T) {
	root := phase7RepositoryRoot(t)
	applicationDockerfiles := map[string][]string{
		"Dockerfile.control-plane": {"USER 65532:65532", "HEALTHCHECK", "org.opencontainers.image.source", "org.opencontainers.image.revision", "--chmod=0555", "migrations /app/migrations", `io.ajiasu.docker-socket="forbidden"`},
		"Dockerfile.gateway":       {"FROM --platform=$TARGETPLATFORM ${RUST_BUILD_IMAGE}", "cargo build --locked", "USER 65532:65532", "HEALTHCHECK", "EXPOSE 8080 1080", `io.ajiasu.docker-socket="forbidden"`},
		"Dockerfile.agent":         {"FROM --platform=$TARGETPLATFORM ${RUST_BUILD_IMAGE}", "cargo build --locked", "USER 65532:65532", "HEALTHCHECK", "EXPOSE 9092", `io.ajiasu.docker-socket="required"`},
		"Dockerfile":               {"USER 65532:65532", "HEALTHCHECK", "org.opencontainers.image.source", "runner-healthcheck.sh", `io.ajiasu.docker-socket="forbidden"`},
	}
	for name, required := range applicationDockerfiles {
		content := string(readPhase7File(t, filepath.Join(root, name)))
		if !strings.Contains(content, "# syntax=docker/dockerfile:1.7@sha256:") {
			t.Errorf("%s does not pin the Dockerfile frontend", name)
		}
		for _, fragment := range required {
			if !strings.Contains(content, fragment) {
				t.Errorf("%s is missing %q", name, fragment)
			}
		}
		for _, forbidden := range []string{"latest", "--privileged", "AJIASU_AGENT_ENROLLMENT_TOKEN=", "AJIASU_GATEWAY_ENROLLMENT_TOKEN="} {
			if strings.Contains(content, forbidden) {
				t.Errorf("%s contains forbidden image contract %q", name, forbidden)
			}
		}
	}
}

func TestPhase7FakeRunnerCannotEnterProductionManifest(t *testing.T) {
	root := phase7RepositoryRoot(t)
	dockerfile := string(readPhase7File(t, filepath.Join(root, "tests", "e2e", "Dockerfile.fake-runner")))
	if !strings.Contains(dockerfile, `io.ajiasu.test-only="true"`) || !strings.Contains(dockerfile, "USER 65532:65532") {
		t.Fatal("fake Runner is not marked test-only and non-root")
	}
	manifest := string(readPhase7File(t, filepath.Join(root, "deploy", "compose", "testdata", "revision-1.json")))
	if strings.Contains(strings.ToLower(manifest), "fake-runner") {
		t.Fatal("production release manifest references the fake Runner")
	}
}

func TestPhase7SBOMInputsCoverReleaseImages(t *testing.T) {
	root := phase7RepositoryRoot(t)
	var document struct {
		Revision  int      `json:"revision"`
		Platforms []string `json:"platforms"`
		Images    []struct {
			Name       string `json:"name"`
			Dockerfile string `json:"dockerfile"`
			Binary     string `json:"binary"`
		} `json:"images"`
		TestImages []struct {
			Name              string `json:"name"`
			Dockerfile        string `json:"dockerfile"`
			ProductionAllowed bool   `json:"production_allowed"`
		} `json:"test_images"`
	}
	if err := json.Unmarshal(readPhase7File(t, filepath.Join(root, "build", "sbom-inputs.json")), &document); err != nil {
		t.Fatal(err)
	}
	if document.Revision != 1 || strings.Join(document.Platforms, ",") != "linux/amd64,linux/arm64" || len(document.Images) != 4 {
		t.Fatalf("invalid SBOM inputs: %#v", document)
	}
	for _, image := range document.Images {
		if image.Name == "" || image.Binary == "" {
			t.Fatalf("invalid SBOM image: %#v", image)
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(image.Dockerfile))); err != nil {
			t.Fatal(err)
		}
	}
	if len(document.TestImages) != 2 {
		t.Fatalf("fake image production contract=%#v", document.TestImages)
	}
	for _, image := range document.TestImages {
		if image.ProductionAllowed || image.Name == "" {
			t.Fatalf("fake image production contract=%#v", image)
		}
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(image.Dockerfile))); err != nil {
			t.Fatal(err)
		}
	}
}

func TestPhase7ImageCIGeneratesSBOMAndProvenance(t *testing.T) {
	root := phase7RepositoryRoot(t)
	content := string(readPhase7File(t, filepath.Join(root, "scripts", "compose-image-ci.ps1")))
	for _, required := range []string{"linux/amd64,linux/arm64", "type=sbom", "type=provenance,mode=max", "--pull=false", "Dockerfile.gateway", "Dockerfile.agent", "ALPINE_IMAGE=", "Dockerfile.fake-runner", "Dockerfile.fake-target", "Get-Command trivy", "& trivy @trivyArguments", "--image-src docker", "--scanners vuln,secret", "image scan failed after 3 attempts", "docker history --no-trunc"} {
		if !strings.Contains(content, required) {
			t.Errorf("compose image CI is missing %q", required)
		}
	}
}

func TestPhase7ComposeModelGateIsCommitted(t *testing.T) {
	root := phase7RepositoryRoot(t)
	content := string(readPhase7File(t, filepath.Join(root, "scripts", "compose-model.test.ps1")))
	for _, required := range []string{"'compose', '--env-file'", "Docker socket leaked", "image is mutable", "root filesystem is writable", "publishes a dependency port", "standing Runner or Console"} {
		if !strings.Contains(content, required) {
			t.Errorf("Compose model gate is missing %q", required)
		}
	}
}

func TestPhase7ComposeLayoutAndGeneratedStateIgnore(t *testing.T) {
	root := phase7RepositoryRoot(t)
	for _, relative := range []string{
		"deploy/compose/compose.yaml",
		"deploy/compose/compose.dependencies.yaml",
		"deploy/compose/compose.development.yaml",
		"deploy/compose/compose.production.yaml",
		"deploy/compose/env/compose.env.example",
	} {
		if _, err := os.Stat(filepath.Join(root, filepath.FromSlash(relative))); err != nil {
			t.Fatal(err)
		}
	}
	gitignore := string(readPhase7File(t, filepath.Join(root, ".gitignore")))
	dockerignore := string(readPhase7File(t, filepath.Join(root, ".dockerignore")))
	if !strings.Contains(gitignore, "deploy/compose/generated/") || !strings.Contains(dockerignore, "deploy/compose/generated") {
		t.Fatal("generated Compose state is not excluded from Git and image contexts")
	}
}
