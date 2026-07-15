package contract_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPhase6SchedulerProtocolFixtureAndReservedFields(t *testing.T) {
	_, source, _, _ := runtime.Caller(0)
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
	fixturePath := filepath.Join(root, "api", "proto", "scheduler", "v1", "testdata", "revision-1.json")
	content, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		ProtocolRevision int      `json:"protocol_revision"`
		RequiredMessages []string `json:"required_messages"`
		AssignmentStates []string `json:"assignment_states"`
		HealthDimensions []string `json:"health_dimensions"`
	}
	if err := json.Unmarshal(content, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.ProtocolRevision != 1 || len(fixture.RequiredMessages) != 5 || len(fixture.AssignmentStates) != 8 || len(fixture.HealthDimensions) != 4 {
		t.Fatalf("invalid scheduler fixture: %+v", fixture)
	}

	schedulerProto, err := os.ReadFile(filepath.Join(root, "api", "proto", "scheduler", "v1", "scheduler.proto"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(schedulerProto)
	for _, required := range []string{"uint64 fencing_token", "reserved 13 to 19", "HEALTH_DIMENSION_ACCOUNT", "ASSIGNMENT_STATE_MIGRATING"} {
		if !strings.Contains(text, required) {
			t.Errorf("scheduler proto missing %q", required)
		}
	}

	gatewayProto, err := os.ReadFile(filepath.Join(root, "api", "proto", "gateway", "v1", "gateway.proto"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"assignment_id = 7", "assignment_generation = 8", "assignment_state = 11", "valid_until = 12"} {
		if !strings.Contains(string(gatewayProto), required) {
			t.Errorf("gateway proto missing %q", required)
		}
	}
}
