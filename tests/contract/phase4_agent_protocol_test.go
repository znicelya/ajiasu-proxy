package contract_test

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestPhase4KeepsCurrentAndPreviousProtocolFixtures(t *testing.T) {
	_, source, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
	for _, expected := range []int{1, 2} {
		path := filepath.Join(root, "api", "proto", "agent", "v1", "testdata", fmt.Sprintf("revision-%d.json", expected))
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var fixture struct {
			ProtocolRevision int      `json:"protocol_revision"`
			RequiredMessages []string `json:"required_messages"`
			RunnerActions    []string `json:"runner_actions"`
		}
		if err := json.Unmarshal(content, &fixture); err != nil {
			t.Fatal(err)
		}
		if fixture.ProtocolRevision != expected {
			t.Fatalf("fixture %s revision=%d", path, fixture.ProtocolRevision)
		}
		if len(fixture.RequiredMessages) != 6 || len(fixture.RunnerActions) != 4 {
			t.Fatalf("fixture %s is incomplete", path)
		}
	}
}
