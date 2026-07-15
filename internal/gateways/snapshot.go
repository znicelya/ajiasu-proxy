package gateways

import (
	"crypto/ed25519"
	"reflect"
	"sort"
	"time"

	"github.com/google/uuid"
)

type SnapshotInput struct {
	TenantID, EndpointID, RunnerID, GatewayID uuid.UUID
	AssignmentID, AccountID, NodeID           uuid.UUID
	Generation, AssignmentGeneration          uint64
	PolicyHash, AssignmentState               string
	Protocols                                 []string
	Credentials                               []CredentialVerifier
	GrantExpiry, ValidUntil                   time.Time
}

func BuildSnapshot(version uint64, now time.Time, signer ed25519.PrivateKey, inputs []SnapshotInput) (Snapshot, error) {
	if version == 0 || len(signer) != ed25519.PrivateKeySize {
		return Snapshot{}, ErrInvalidArgument
	}
	routes := make([]Route, 0, len(inputs))
	for _, input := range inputs {
		if input.TenantID == uuid.Nil || input.EndpointID == uuid.Nil || input.RunnerID == uuid.Nil || input.GatewayID == uuid.Nil || input.AssignmentID == uuid.Nil || input.AccountID == uuid.Nil || input.NodeID == uuid.Nil || input.Generation == 0 || input.AssignmentGeneration != input.Generation || input.PolicyHash == "" || !input.GrantExpiry.After(now.UTC()) || !input.ValidUntil.After(now.UTC()) || len(input.Protocols) == 0 || !validAssignmentState(input.AssignmentState) {
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
		routes = append(routes, Route{TenantID: input.TenantID, EndpointID: input.EndpointID, PolicyHash: input.PolicyHash, Protocols: protocols, Credentials: credentials, Grant: grant, AssignmentID: input.AssignmentID, AssignmentGeneration: input.AssignmentGeneration, AccountID: input.AccountID, NodeID: input.NodeID, AssignmentState: input.AssignmentState, ValidUntil: input.ValidUntil.UTC()})
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
		if err := validateCommittedRoute(route); err != nil {
			return err
		}
		if current, ok := a.routes[routeKey(route.TenantID, route.EndpointID)]; ok && route.AssignmentGeneration < current.AssignmentGeneration {
			return ErrStaleAssignment
		}
		routes[routeKey(route.TenantID, route.EndpointID)] = route
	}
	if snapshot.Version == a.version {
		if reflect.DeepEqual(routes, a.routes) {
			return nil
		}
		return ErrStaleVersion
	}
	a.routes, a.version = routes, snapshot.Version
	return nil
}
func (a *SnapshotApplier) ApplyDelta(delta Delta) error {
	if delta.Version < a.version {
		return ErrStaleVersion
	}
	key := routeKey(delta.Route.TenantID, delta.Route.EndpointID)
	if delta.Version == a.version {
		current, ok := a.routes[key]
		if (delta.Revoked && !ok) || (!delta.Revoked && ok && reflect.DeepEqual(current, delta.Route)) {
			return nil
		}
		return ErrStaleVersion
	}
	if a.version != 0 && delta.Version != a.version+1 {
		return ErrSnapshotRequired
	}
	if delta.Route.TenantID == uuid.Nil || delta.Route.EndpointID == uuid.Nil {
		return ErrInvalidArgument
	}
	if current, ok := a.routes[key]; ok && delta.Route.AssignmentGeneration < current.AssignmentGeneration {
		return ErrStaleAssignment
	}
	if delta.Revoked {
		delete(a.routes, key)
	} else {
		if err := validateCommittedRoute(delta.Route); err != nil {
			return err
		}
		a.routes[key] = delta.Route
	}
	a.version = delta.Version
	return nil
}
func (a *SnapshotApplier) Version() uint64 { return a.version }
func (a *SnapshotApplier) Route(tenantID, endpointID uuid.UUID) (Route, bool) {
	route, ok := a.routes[routeKey(tenantID, endpointID)]
	return route, ok
}
func (a *SnapshotApplier) Select(tenantID, endpointID uuid.UUID, now time.Time) (Route, error) {
	route, ok := a.Route(tenantID, endpointID)
	if !ok {
		return Route{}, ErrRouteUnavailable
	}
	if route.AssignmentState != "assigned" || !route.ValidUntil.After(now.UTC()) {
		return Route{}, ErrRouteUnavailable
	}
	// Signature verification belongs to the transport adapter; this cache
	// enforces the assignment/grant metadata needed before route selection.
	if err := validateCommittedRoute(route); err != nil || !route.Grant.ExpiresAt.After(now.UTC()) {
		return Route{}, ErrInvalidGrant
	}
	return route, nil
}
func validAssignmentState(state string) bool { return state == "assigned" || state == "draining" }
func validateCommittedRoute(route Route) error {
	if route.TenantID == uuid.Nil || route.EndpointID == uuid.Nil || route.AssignmentID == uuid.Nil || route.AccountID == uuid.Nil || route.NodeID == uuid.Nil || route.Grant.RunnerID == uuid.Nil || route.Grant.TenantID != route.TenantID || route.Grant.EndpointID != route.EndpointID || route.AssignmentGeneration == 0 || route.Grant.Generation != route.AssignmentGeneration || route.PolicyHash == "" || route.Grant.PolicyHash != route.PolicyHash || !validAssignmentState(route.AssignmentState) || route.ValidUntil.IsZero() || route.Grant.ExpiresAt.IsZero() {
		return ErrInvalidArgument
	}
	return nil
}
func routeKey(tenantID, endpointID uuid.UUID) string {
	return tenantID.String() + "/" + endpointID.String()
}
