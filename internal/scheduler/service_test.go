package scheduler

import (
	"errors"
	"github.com/google/uuid"
	"testing"
)

func TestReconcileCommandValidation(t *testing.T) {
	valid := ReconcileCommand{EndpointID: uuid.New(), ExpectedEndpointVersion: 1, ReasonCode: "operator_reconcile"}
	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	valid.ExpectedEndpointVersion = 0
	if !errors.Is(valid.Validate(), ErrSchedulerInvalid) {
		t.Fatal("accepted zero version")
	}
	valid.ExpectedEndpointVersion = 1
	valid.ReasonCode = ""
	if !errors.Is(valid.Validate(), ErrSchedulerInvalid) {
		t.Fatal("accepted empty reason")
	}
}
