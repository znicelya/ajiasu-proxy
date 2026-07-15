package scheduler

import "strings"

func eligible(request SelectionRequest, candidate Candidate) bool {
	if candidate.TenantID != request.TenantID || candidate.PoolID != request.PoolID || candidate.MembershipID == [16]byte{} || candidate.AccountID == [16]byte{} || candidate.NodeID == [16]byte{} {
		return false
	}
	if !candidate.MembershipEnabled || candidate.AccountState != "active" || candidate.AccountHealth == HealthUnhealthy || candidate.AccountHealth == HealthQuarantined {
		return false
	}
	if candidate.MembershipExpiresAt != nil && !candidate.MembershipExpiresAt.After(request.Now) {
		return false
	}
	if candidate.CooldownUntil != nil && candidate.CooldownUntil.After(request.Now) {
		return false
	}
	if candidate.MaxConcurrency <= 0 || candidate.ReservedConcurrency >= candidate.MaxConcurrency {
		return false
	}
	if candidate.NodeMaintenance != "active" || candidate.NodeConnectivity != "online" || candidate.NodeMaxRunners <= 0 || candidate.NodeActiveRunners >= candidate.NodeMaxRunners-candidate.NodeReservedHeadroom {
		return false
	}
	if required := strings.TrimSpace(request.RequiredArchitecture); required != "" && candidate.NodeArchitecture != required {
		return false
	}
	for _, capability := range request.RequiredCapabilities {
		if !candidate.NodeCapabilities[capability] {
			return false
		}
	}
	for key, value := range request.Selector {
		if candidate.AccountLabels[key] != value {
			return false
		}
	}
	return true
}

func filterEligible(request SelectionRequest, candidates []Candidate) []Candidate {
	result := make([]Candidate, 0, len(candidates))
	for _, candidate := range candidates {
		if eligible(request, candidate) {
			result = append(result, candidate)
		}
	}
	return result
}
