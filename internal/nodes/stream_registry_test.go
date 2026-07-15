package nodes

import (
	"testing"

	agentv1 "github.com/dnomd343/ajiasu-proxy/internal/gen/agent/v1"
	"github.com/google/uuid"
)

func TestStreamRegistryClonesAndClearsCredentialPayload(t *testing.T) {
	registry := NewStreamRegistry()
	nodeID := uuid.New()
	registration, _ := registry.Attach(nodeID)
	defer registry.Detach(nodeID, registration)
	original := &agentv1.ControlMessage{Body: &agentv1.ControlMessage_RunnerCommand{RunnerCommand: &agentv1.RunnerCommand{CredentialConfiguration: []byte("credential-canary")}}}
	if err := registry.Deliver(t.Context(), nodeID, original); err != nil {
		t.Fatal(err)
	}
	clear(original.GetRunnerCommand().CredentialConfiguration)
	queued := <-registration.queue
	if string(queued.GetRunnerCommand().GetCredentialConfiguration()) != "credential-canary" {
		t.Fatal("delivery did not own an independent credential buffer")
	}
	clearControlMessage(queued)
	if len(queued.GetRunnerCommand().GetCredentialConfiguration()) != 0 {
		t.Fatal("sent credential buffer was not cleared")
	}
}
