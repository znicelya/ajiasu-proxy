package health

import "time"

func Evaluate(current State, observation Observation, config Config) (State, *Transition, error) {
	if observation.Validate() != nil || config.Validate() != nil {
		return State{}, nil, ErrInvalidObservation
	}
	if current.ResourceID != [16]byte{} {
		if current.TenantID != observation.TenantID || current.ResourceType != observation.ResourceType || current.ResourceID != observation.ResourceID || current.Dimension != observation.Dimension {
			return State{}, nil, ErrInvalidObservation
		}
		if observation.Generation < current.Generation || (observation.Generation == current.Generation && observation.Sequence <= current.LastSequence) {
			return current, nil, ErrStaleObservation
		}
	} else {
		current = State{TenantID: observation.TenantID, ResourceType: observation.ResourceType, ResourceID: observation.ResourceID, Dimension: observation.Dimension, Status: StatusUnknown, LastTransitionAt: observation.ObservedAt}
	}

	previous := current.Status
	current.Generation, current.LastSequence = observation.Generation, observation.Sequence
	current.ReasonCode, current.LastObservedAt = observation.ReasonCode, observation.ObservedAt.UTC()
	if observation.Result == ResultSuccess {
		current.ConsecutiveSuccesses++
		current.ConsecutiveFailures = 0
		if current.ConsecutiveSuccesses >= config.RecoverySuccesses && cooldownElapsed(current.CooldownUntil, observation.ObservedAt) {
			current.Status = StatusHealthy
			current.CooldownUntil = nil
		}
	} else {
		current.ConsecutiveFailures++
		current.ConsecutiveSuccesses = 0
		if observation.Dimension == DimensionAccount {
			if current.ConsecutiveFailures >= config.AccountQuarantineFailures {
				current.Status = StatusQuarantined
				current.QuarantineCount++
				until := observation.ObservedAt.Add(backoff(config.CooldownBase, config.CooldownMaximum, current.QuarantineCount))
				current.CooldownUntil = &until
			} else {
				current.Status = StatusDegraded
			}
		} else {
			threshold := thresholdFor(config, observation.Dimension)
			if current.ConsecutiveFailures >= threshold.UnhealthyFailures {
				current.Status = StatusUnhealthy
			} else if current.ConsecutiveFailures >= threshold.DegradedFailures {
				current.Status = StatusDegraded
			}
		}
	}
	if current.Status == previous {
		return current, nil, nil
	}
	current.LastTransitionAt = observation.ObservedAt.UTC()
	return current, &Transition{From: previous, To: current.Status, Dimension: current.Dimension, ReasonCode: current.ReasonCode, Generation: current.Generation, Sequence: current.LastSequence, OccurredAt: current.LastTransitionAt}, nil
}

func thresholdFor(config Config, dimension Dimension) Threshold {
	switch dimension {
	case DimensionProcess:
		return config.Process
	case DimensionTunnel:
		return config.Tunnel
	default:
		return config.Egress
	}
}
func cooldownElapsed(until *time.Time, now time.Time) bool { return until == nil || !until.After(now) }
func backoff(base, maximum time.Duration, count int) time.Duration {
	value := base
	for i := 1; i < count && value < maximum; i++ {
		if value > maximum/2 {
			return maximum
		}
		value *= 2
	}
	if value > maximum {
		return maximum
	}
	return value
}
