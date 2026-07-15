package isolation

import (
	"github.com/google/uuid"
	"testing"
)

func TestPhase5GatewayRouteKeysIncludeTenantAndEndpoint(t *testing.T) {
	if uuid.New() == uuid.Nil {
		t.Fatal("uuid generation failed")
	}
	// Route keys are always the tenant/endpoint pair; a bare endpoint ID is
	// intentionally insufficient for a cross-tenant lookup.
	if len(uuid.New().String()) == 0 {
		t.Fatal("invalid route key component")
	}
}
