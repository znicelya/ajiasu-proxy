package health

import (
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
)

func observationFixture(dimension Dimension, result Result, sequence uint64, now time.Time) Observation {
	return Observation{TenantID: uuid.New(), ResourceType: "account", ResourceID: uuid.New(), Dimension: dimension, Result: result, Generation: 1, Sequence: sequence, ReasonCode: "probe_result", ObservedAt: now}
}

func TestHealthDebouncesAndRejectsStaleReports(t *testing.T) {
	config := DefaultConfig()
	now := time.Unix(100, 0).UTC()
	observation := observationFixture(DimensionProcess, ResultFailure, 1, now)
	state, transition, err := Evaluate(State{}, observation, config)
	if err != nil || transition != nil || state.Status != StatusUnknown {
		t.Fatalf("state=%+v transition=%+v err=%v", state, transition, err)
	}
	observation.Sequence = 2
	state, transition, err = Evaluate(state, observation, config)
	if err != nil || transition == nil || state.Status != StatusDegraded {
		t.Fatalf("state=%+v transition=%+v err=%v", state, transition, err)
	}
	if _, _, err = Evaluate(state, observation, config); !errors.Is(err, ErrStaleObservation) {
		t.Fatalf("stale error=%v", err)
	}
	observation.Sequence = 3
	state, transition, err = Evaluate(state, observation, config)
	if err != nil || transition == nil || state.Status != StatusUnhealthy {
		t.Fatalf("state=%+v", state)
	}
}

func TestAccountQuarantineCooldownAndRecoveryHysteresis(t *testing.T) {
	config := DefaultConfig()
	config.CooldownBase = time.Second
	config.CooldownMaximum = 4 * time.Second
	now := time.Unix(100, 0).UTC()
	observation := observationFixture(DimensionAccount, ResultFailure, 1, now)
	var state State
	for sequence := uint64(1); sequence <= 3; sequence++ {
		observation.Sequence = sequence
		observation.ObservedAt = now.Add(time.Duration(sequence) * time.Millisecond)
		state, _, _ = Evaluate(state, observation, config)
	}
	if state.Status != StatusQuarantined || state.CooldownUntil == nil {
		t.Fatalf("state=%+v", state)
	}
	for sequence := uint64(4); sequence <= 6; sequence++ {
		observation.Sequence = sequence
		observation.Result = ResultSuccess
		observation.ObservedAt = now.Add(500 * time.Millisecond)
		state, _, _ = Evaluate(state, observation, config)
	}
	if state.Status != StatusQuarantined {
		t.Fatalf("recovered before cooldown: %+v", state)
	}
	observation.Sequence = 7
	observation.ObservedAt = now.Add(2 * time.Second)
	state, transition, err := Evaluate(state, observation, config)
	if err != nil || transition == nil || state.Status != StatusHealthy {
		t.Fatalf("state=%+v transition=%+v err=%v", state, transition, err)
	}
}

func TestHealthRejectsWrongResourceAndGeneration(t *testing.T) {
	config := DefaultConfig()
	now := time.Now().UTC()
	observation := observationFixture(DimensionTunnel, ResultSuccess, 1, now)
	observation.Generation = 2
	state, _, _ := Evaluate(State{}, observation, config)
	other := observation
	other.ResourceID = uuid.New()
	other.Sequence = 2
	if _, _, err := Evaluate(state, other, config); !errors.Is(err, ErrInvalidObservation) {
		t.Fatalf("error=%v", err)
	}
	observation.Generation = 1
	observation.Sequence = 2
	if _, _, err := Evaluate(state, observation, config); !errors.Is(err, ErrStaleObservation) {
		t.Fatalf("generation error=%v", err)
	}
}
