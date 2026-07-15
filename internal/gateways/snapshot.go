package gateways

import (
	"crypto/ed25519"
	"sort"
	"time"

	"github.com/google/uuid"
)

type SnapshotInput struct {
	TenantID, EndpointID, RunnerID, GatewayID uuid.UUID
	Generation                                uint64
	PolicyHash                                string
	Protocols                                 []string
	Credentials                               []CredentialVerifier
	GrantExpiry                               time.Time
}

func BuildSnapshot(version uint64, now time.Time, signer ed25519.PrivateKey, inputs []SnapshotInput) (Snapshot, error) {
	if version == 0 || len(signer) != ed25519.PrivateKeySize {
		return Snapshot{}, ErrInvalidArgument
	}
	routes := make([]Route, 0, len(inputs))
	for _, input := range inputs {
		if input.TenantID == uuid.Nil || input.EndpointID == uuid.Nil || input.RunnerID == uuid.Nil || input.GatewayID == uuid.Nil || input.Generation == 0 || input.PolicyHash == "" || !input.GrantExpiry.After(now.UTC()) || len(input.Protocols) == 0 {
			continue
		}
		protocols := append([]string(nil), input.Protocols...)
		sort.Strings(protocols)
		valid := true
		for index, protocol := range protocols {
			if (protocol != "http" && protocol != "connect" && protocol != "socks5") || (index > 0 && protocols[index-1] == protocol) {
				valid = false
				break
			}
		}
		if !valid {
			continue
		}
		grant := (RouteGrant{GatewayID: input.GatewayID, TenantID: input.TenantID, EndpointID: input.EndpointID, RunnerID: input.RunnerID, Generation: input.Generation, Protocols: protocols, PolicyHash: input.PolicyHash, ExpiresAt: input.GrantExpiry}).Sign(signer)
		credentials := append([]CredentialVerifier(nil), input.Credentials...)
		sort.Slice(credentials, func(i, j int) bool { return credentials[i].PublicIdentifier < credentials[j].PublicIdentifier })
		routes = append(routes, Route{TenantID: input.TenantID, EndpointID: input.EndpointID, PolicyHash: input.PolicyHash, Protocols: protocols, Credentials: credentials, Grant: grant})
	}
	sort.Slice(routes, func(i, j int) bool {
		if routes[i].TenantID == routes[j].TenantID {
			return routes[i].EndpointID.String() < routes[j].EndpointID.String()
		}
		return routes[i].TenantID.String() < routes[j].TenantID.String()
	})
	return Snapshot{Version: version, GeneratedAt: now.UTC(), Routes: routes}, nil
}

type SnapshotApplier struct {
	version uint64
	routes  map[string]Route
}

func NewSnapshotApplier() *SnapshotApplier { return &SnapshotApplier{routes: map[string]Route{}} }
func (a *SnapshotApplier) ApplySnapshot(snapshot Snapshot) error {
	if snapshot.Version == 0 || snapshot.Version < a.version {
		return ErrStaleVersion
	}
	routes := map[string]Route{}
	for _, route := range snapshot.Routes {
		routes[routeKey(route.TenantID, route.EndpointID)] = route
	}
	a.routes, a.version = routes, snapshot.Version
	return nil
}
func (a *SnapshotApplier) ApplyDelta(delta Delta) error {
	if delta.Version <= a.version {
		return ErrStaleVersion
	}
	if delta.Revoked {
		delete(a.routes, routeKey(delta.Route.TenantID, delta.Route.EndpointID))
	} else {
		a.routes[routeKey(delta.Route.TenantID, delta.Route.EndpointID)] = delta.Route
	}
	a.version = delta.Version
	return nil
}
func (a *SnapshotApplier) Version() uint64 { return a.version }
func (a *SnapshotApplier) Route(tenantID, endpointID uuid.UUID) (Route, bool) {
	route, ok := a.routes[routeKey(tenantID, endpointID)]
	return route, ok
}
func routeKey(tenantID, endpointID uuid.UUID) string {
	return tenantID.String() + "/" + endpointID.String()
}
