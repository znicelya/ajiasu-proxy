package endpoints

import (
	"errors"
	"testing"

	"github.com/google/uuid"
)

func TestPhase6EndpointBindingValidation(t *testing.T) {
	fixed := CreateCommand{Name: "fixed", BindingMode: "fixed", AccountID: uuid.New(), NodeID: uuid.New(), DesiredRunnerState: DesiredRunning}
	if err := fixed.Validate(); err != nil {
		t.Fatal(err)
	}
	pool := CreateCommand{Name: "pool", BindingMode: "pool", PoolID: uuid.New(), DesiredRunnerState: DesiredRunning}
	if err := pool.Validate(); err != nil {
		t.Fatal(err)
	}
	pool.AccountID = uuid.New()
	if !errors.Is(pool.Validate(), ErrInvalidArgument) {
		t.Fatal("pool binding accepted direct account")
	}
	fixed.PoolID = uuid.New()
	if !errors.Is(fixed.Validate(), ErrInvalidArgument) {
		t.Fatal("fixed binding accepted pool")
	}
}
