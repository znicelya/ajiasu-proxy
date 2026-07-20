package gateways

import (
	"context"
	"crypto/ed25519"
	"errors"
	"time"

	"github.com/znicelya/ajiasu-proxy/internal/platform/database"
	"github.com/google/uuid"
)

const routeGrantLifetime = 5 * time.Minute

type DatabaseSnapshotProvider struct {
	pools  *database.Pools
	signer ed25519.PrivateKey
	now    func() time.Time
}

func NewDatabaseSnapshotProvider(pools *database.Pools, seed []byte) (*DatabaseSnapshotProvider, error) {
	if pools == nil || pools.Platform == nil || len(seed) != ed25519.SeedSize {
		return nil, ErrInvalidArgument
	}
	privateKey := ed25519.NewKeyFromSeed(seed)
	return &DatabaseSnapshotProvider{
		pools:  pools,
		signer: privateKey,
		now:    func() time.Time { return time.Now().UTC() },
	}, nil
}

func (p *DatabaseSnapshotProvider) Snapshot(ctx context.Context, gatewayID uuid.UUID) (Snapshot, error) {
	if p == nil || gatewayID == uuid.Nil {
		return Snapshot{}, ErrInvalidArgument
	}
	now := p.now().UTC()
	rows, err := p.pools.Platform.Query(ctx, `
SELECT assignment.tenant_id,
       assignment.endpoint_id,
       assignment.assignment_id,
       assignment.account_id,
       assignment.node_id,
       assignment.runner_id,
       assignment.desired_generation,
       assignment.valid_until,
       profile.policy_hash,
       profile.protocols,
       credential.id,
       credential.public_identifier,
       credential.verifier,
       credential.expires_at
FROM scheduler.endpoint_assignments AS assignment
JOIN endpoints.proxy_endpoints AS endpoint
  ON endpoint.tenant_id = assignment.tenant_id
 AND endpoint.id = assignment.endpoint_id
JOIN endpoints.access_profiles AS profile
  ON profile.tenant_id = assignment.tenant_id
 AND profile.endpoint_id = assignment.endpoint_id
LEFT JOIN endpoints.proxy_credentials AS credential
  ON credential.tenant_id = assignment.tenant_id
 AND credential.endpoint_id = assignment.endpoint_id
 AND credential.revoked_at IS NULL
 AND (credential.expires_at IS NULL OR credential.expires_at > $1)
WHERE assignment.state = 'assigned'
  AND assignment.valid_until > $1
  AND endpoint.lifecycle_state = 'active'
  AND endpoint.desired_runner_state = 'running'
ORDER BY assignment.tenant_id, assignment.endpoint_id, credential.public_identifier, credential.id
`, now)
	if err != nil {
		return Snapshot{}, err
	}
	defer rows.Close()

	inputs := make([]SnapshotInput, 0)
	indexes := make(map[string]int)
	for rows.Next() {
		var (
			tenantID, endpointID, assignmentID uuid.UUID
			accountID, nodeID, runnerID        uuid.UUID
			generation                         int64
			validUntil                         time.Time
			policyHash                         string
			protocols                          []string
			credentialID                       *uuid.UUID
			publicIdentifier, verifier         *string
			credentialExpiry                   *time.Time
		)
		if err := rows.Scan(&tenantID, &endpointID, &assignmentID, &accountID, &nodeID, &runnerID, &generation, &validUntil, &policyHash, &protocols, &credentialID, &publicIdentifier, &verifier, &credentialExpiry); err != nil {
			return Snapshot{}, err
		}
		if generation <= 0 {
			continue
		}
		key := routeKey(tenantID, endpointID)
		index, exists := indexes[key]
		if !exists {
			grantExpiry := now.Add(routeGrantLifetime)
			if validUntil.Before(grantExpiry) {
				grantExpiry = validUntil
			}
			// Whole-second grant expiries have an identical RFC 3339 canonical
			// representation in the Go signer and Rust verifier.
			grantExpiry = grantExpiry.UTC().Truncate(time.Second)
			if !grantExpiry.After(now) {
				continue
			}
			inputs = append(inputs, SnapshotInput{
				GatewayID: gatewayID, TenantID: tenantID, EndpointID: endpointID,
				RunnerID: runnerID, AssignmentID: assignmentID, AccountID: accountID, NodeID: nodeID,
				Generation: uint64(generation), AssignmentGeneration: uint64(generation),
				PolicyHash: policyHash, Protocols: protocols, AssignmentState: "assigned",
				GrantExpiry: grantExpiry, ValidUntil: validUntil.UTC(),
			})
			index = len(inputs) - 1
			indexes[key] = index
		}
		if credentialID != nil && publicIdentifier != nil && verifier != nil {
			inputs[index].Credentials = append(inputs[index].Credentials, CredentialVerifier{
				ID: *credentialID, PublicIdentifier: *publicIdentifier, Verifier: *verifier,
				ExpiresAt: credentialExpiry,
			})
		}
	}
	if err := rows.Err(); err != nil {
		return Snapshot{}, err
	}
	var version int64
	if err := p.pools.Platform.QueryRow(ctx, `UPDATE gateways.gateways SET version=version+1,updated_at=$1 WHERE id=$2 AND state='active' RETURNING version`, now, gatewayID).Scan(&version); err != nil || version <= 0 {
		if err != nil {
			return Snapshot{}, err
		}
		return Snapshot{}, ErrInvalidArgument
	}
	snapshot, err := BuildSnapshot(uint64(version), now, p.signer, inputs)
	if err != nil {
		return Snapshot{}, err
	}
	if len(snapshot.Routes) != len(inputs) {
		return Snapshot{}, errors.New("committed gateway routes failed validation")
	}
	return snapshot, nil
}
