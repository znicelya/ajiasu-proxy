package scheduler

import (
	"cmp"
	"sort"
)

func SelectCandidate(request SelectionRequest, candidates []Candidate) (Selection, error) {
	if request.Validate() != nil || len(candidates) == 0 || len(candidates) > 10000 {
		return Selection{}, ErrSchedulerInvalid
	}
	eligibleCandidates := filterEligible(request, candidates)
	if len(eligibleCandidates) == 0 {
		for _, candidate := range candidates {
			if candidate.TenantID == request.TenantID && candidate.PoolID == request.PoolID && candidate.MembershipEnabled && candidate.AccountState == "active" && candidate.ReservedConcurrency >= candidate.MaxConcurrency {
				return Selection{}, ErrCapacityExhausted
			}
		}
		return Selection{}, ErrNoEligibleCandidate
	}

	switch request.Strategy {
	case StrategyLeastConnections:
		sort.SliceStable(eligibleCandidates, func(i, j int) bool { return compareLeastConnections(eligibleCandidates[i], eligibleCandidates[j]) < 0 })
		return Selection{Candidate: eligibleCandidates[0], NextCursor: request.RoundRobinCursor}, nil
	case StrategyFixedPriority:
		sort.SliceStable(eligibleCandidates, func(i, j int) bool { return compareFixedPriority(eligibleCandidates[i], eligibleCandidates[j]) < 0 })
		return Selection{Candidate: eligibleCandidates[0], NextCursor: request.RoundRobinCursor}, nil
	case StrategyRoundRobin:
		sort.SliceStable(eligibleCandidates, func(i, j int) bool {
			if order := cmp.Compare(eligibleCandidates[i].MembershipID.String(), eligibleCandidates[j].MembershipID.String()); order != 0 {
				return order < 0
			}
			return eligibleCandidates[i].AccountID.String() < eligibleCandidates[j].AccountID.String()
		})
		index := request.RoundRobinCursor % uint64(len(eligibleCandidates))
		return Selection{Candidate: eligibleCandidates[index], NextCursor: request.RoundRobinCursor + 1}, nil
	default:
		return Selection{}, ErrSchedulerInvalid
	}
}

func compareLeastConnections(left, right Candidate) int {
	if order := cmp.Compare(left.ActiveConnections, right.ActiveConnections); order != 0 {
		return order
	}
	if order := compareReservedRatio(left, right); order != 0 {
		return order
	}
	if order := cmp.Compare(left.Priority, right.Priority); order != 0 {
		return order
	}
	if order := cmp.Compare(left.AccountID.String(), right.AccountID.String()); order != 0 {
		return order
	}
	return cmp.Compare(left.NodeID.String(), right.NodeID.String())
}

func compareFixedPriority(left, right Candidate) int {
	if order := cmp.Compare(left.Priority, right.Priority); order != 0 {
		return order
	}
	if order := cmp.Compare(healthRank(left.AccountHealth), healthRank(right.AccountHealth)); order != 0 {
		return order
	}
	if order := compareReservedRatio(left, right); order != 0 {
		return order
	}
	return cmp.Compare(left.AccountID.String(), right.AccountID.String())
}

func compareReservedRatio(left, right Candidate) int {
	leftValue := int64(left.ReservedConcurrency) * int64(right.MaxConcurrency)
	rightValue := int64(right.ReservedConcurrency) * int64(left.MaxConcurrency)
	return cmp.Compare(leftValue, rightValue)
}

func healthRank(state HealthState) int {
	switch state {
	case HealthHealthy:
		return 0
	case HealthUnknown:
		return 1
	case HealthDegraded:
		return 2
	default:
		return 3
	}
}
