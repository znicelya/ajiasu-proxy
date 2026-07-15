package contract_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestPhase5ProtocolFixtures(t *testing.T) {
	_, source, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
	for _, path := range []string{
		filepath.Join(root, "api", "proto", "gateway", "v1", "testdata", "revision-1.json"),
		filepath.Join(root, "api", "proto", "relay", "v1", "testdata", "revision-1.json"),
	} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var fixture map[string]any
		if err := json.Unmarshal(content, &fixture); err != nil {
			t.Fatal(err)
		}
		if fixture["protocol_revision"] != float64(1) {
			t.Fatalf("fixture %s is not revision 1", path)
		}
		if len(fixture["required_messages"].([]any)) == 0 {
			t.Fatalf("fixture %s has no required messages", path)
		}
	}
}
