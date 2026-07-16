package scheduler

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestMigrationPlansAreBoundedAndFenced(t *testing.T) {
	account, node := uuid.New(), uuid.New()
	assignment := Assignment{AssignmentID: uuid.New(), EndpointID: uuid.New(), AccountID: &account, NodeID: &node, DesiredGeneration: 4, FencingToken: 10}
	failure := Failure{Class: FailureNode, ReasonCode: "node_offline", ObservedAt: time.Unix(100, 0).UTC()}
	policy := DefaultMigrationPolicy()
	plan, err := PlanMigration(assignment, "pool", failure, 11, 2, policy)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Type != MigrationNode || plan.DesiredGeneration != 5 || plan.CooldownUntil.Sub(failure.ObservedAt) != 2*time.Second {
		t.Fatalf("plan=%+v", plan)
	}
	if _, err := PlanMigration(assignment, "fixed", failure, 11, 1, policy); !errors.Is(err, ErrFixedEndpointMigration) {
		t.Fatalf("fixed err=%v", err)
	}
	if _, err := PlanMigration(assignment, "pool", failure, 9, 1, policy); !errors.Is(err, ErrStaleFencingToken) {
		t.Fatalf("fence err=%v", err)
	}
	if _, err := PlanMigration(assignment, "pool", failure, 11, 4, policy); !errors.Is(err, ErrMigrationExhausted) {
		t.Fatalf("budget err=%v", err)
	}
}
func TestReplacementRequiresMatchingGenerationFenceAndPublishedRoute(t *testing.T) {
	plan := MigrationPlan{AssignmentID: uuid.New(), DesiredGeneration: 3, FencingToken: 8}
	observation := ReplacementObservation{AssignmentID: plan.AssignmentID, RunnerID: uuid.New(), Generation: 3, FencingToken: 8, Running: true, RoutePublished: true}
	if err := ValidateReplacement(plan, observation); err != nil {
		t.Fatal(err)
	}
	observation.RoutePublished = false
	if !errors.Is(ValidateReplacement(plan, observation), ErrReplacementNotReady) {
		t.Fatal("accepted unpublished route")
	}
}
