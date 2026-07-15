package proxyaccess

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"sort"
	"strings"

	"golang.org/x/net/idna"
)

type Protocol string

const (
	ProtocolHTTP    Protocol = "http"
	ProtocolCONNECT Protocol = "connect"
	ProtocolSOCKS5  Protocol = "socks5"
	DNSGateway               = "gateway"
	DNSRunner                = "runner"
)

var (
	ErrInvalidPolicy    = errors.New("invalid_policy")
	ErrProtocolDenied   = errors.New("protocol_denied")
	ErrSourceDenied     = errors.New("source_denied")
	ErrInvalidTarget    = errors.New("invalid_target")
	ErrPortDenied       = errors.New("port_denied")
	ErrPlatformDenied   = errors.New("platform_denied")
	ErrExplicitDenied   = errors.New("explicit_denied")
	ErrTargetNotAllowed = errors.New("target_not_allowed")
	ErrDNSNoAnswers     = errors.New("dns_no_answers")
)

type PortRange struct {
	From uint16 `json:"from"`
	To   uint16 `json:"to"`
}

type Limits struct {
	MaxConnections        uint32 `json:"max_connections"`
	MaxConnectionRate     uint32 `json:"max_connection_rate"`
	IdleTimeoutSeconds    uint32 `json:"idle_timeout_seconds"`
	MaxBytesPerConnection uint64 `json:"max_bytes_per_connection"`
	TrafficWindowSeconds  uint32 `json:"traffic_window_seconds"`
	MaxWindowBytes        uint64 `json:"max_window_bytes"`
}

type Policy struct {
	Protocols          []Protocol  `json:"protocols"`
	DNSMode            string      `json:"dns_mode"`
	SourceCIDRs        []string    `json:"source_cidrs"`
	TargetAllowCIDRs   []string    `json:"target_allow_cidrs"`
	TargetDenyCIDRs    []string    `json:"target_deny_cidrs"`
	TargetAllowDomains []string    `json:"target_allow_domains"`
	TargetDenyDomains  []string    `json:"target_deny_domains"`
	AllowedPorts       []PortRange `json:"allowed_ports"`
	Limits             Limits      `json:"limits"`
}

type CompiledPolicy struct {
	Policy        Policy
	CanonicalJSON []byte
	Hash          string
	source        []netip.Prefix
	allowCIDRs    []netip.Prefix
	denyCIDRs     []netip.Prefix
}

type Target struct {
	Host     string
	Port     uint16
	Resolved []netip.Addr
}

func DefaultPolicy() Policy {
	return Policy{
		Protocols: []Protocol{ProtocolHTTP}, DNSMode: DNSGateway,
		Limits: Limits{MaxConnections: 100, MaxConnectionRate: 50, IdleTimeoutSeconds: 300},
	}
}

func CompilePolicyJSON(raw []byte) (*CompiledPolicy, error) {
	policy := DefaultPolicy()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&policy); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidPolicy, err)
	}
	if decoder.Decode(&struct{}{}) == nil {
		return nil, fmt.Errorf("%w: multiple json values", ErrInvalidPolicy)
	}
	return CompilePolicy(policy)
}

func CompilePolicy(policy Policy) (*CompiledPolicy, error) {
	if len(policy.Protocols) == 0 {
		return nil, fmt.Errorf("%w: protocols must not be empty", ErrInvalidPolicy)
	}
	protocolSet := map[Protocol]bool{}
	for _, protocol := range policy.Protocols {
		if protocol != ProtocolHTTP && protocol != ProtocolCONNECT && protocol != ProtocolSOCKS5 {
			return nil, fmt.Errorf("%w: unsupported protocol", ErrInvalidPolicy)
		}
		protocolSet[protocol] = true
	}
	policy.Protocols = policy.Protocols[:0]
	for _, protocol := range []Protocol{ProtocolHTTP, ProtocolCONNECT, ProtocolSOCKS5} {
		if protocolSet[protocol] {
			policy.Protocols = append(policy.Protocols, protocol)
		}
	}
	if policy.DNSMode != DNSGateway && policy.DNSMode != DNSRunner {
		return nil, fmt.Errorf("%w: invalid dns mode", ErrInvalidPolicy)
	}
	var err error
	compiled := &CompiledPolicy{Policy: policy}
	if compiled.Policy.SourceCIDRs, compiled.source, err = normalizeCIDRs(policy.SourceCIDRs); err != nil {
		return nil, err
	}
	if compiled.Policy.TargetAllowCIDRs, compiled.allowCIDRs, err = normalizeCIDRs(policy.TargetAllowCIDRs); err != nil {
		return nil, err
	}
	if compiled.Policy.TargetDenyCIDRs, compiled.denyCIDRs, err = normalizeCIDRs(policy.TargetDenyCIDRs); err != nil {
		return nil, err
	}
	if compiled.Policy.TargetAllowDomains, err = normalizeDomains(policy.TargetAllowDomains); err != nil {
		return nil, err
	}
	if compiled.Policy.TargetDenyDomains, err = normalizeDomains(policy.TargetDenyDomains); err != nil {
		return nil, err
	}
	compiled.Policy.AllowedPorts, err = normalizePorts(policy.AllowedPorts)
	if err != nil {
		return nil, err
	}
	if err := validateLimits(compiled.Policy.Limits); err != nil {
		return nil, err
	}
	for _, allow := range compiled.Policy.TargetAllowDomains {
		if contains(compiled.Policy.TargetDenyDomains, allow) {
			return nil, fmt.Errorf("%w: contradictory domain", ErrInvalidPolicy)
		}
	}
	for _, allow := range compiled.Policy.TargetAllowCIDRs {
		if contains(compiled.Policy.TargetDenyCIDRs, allow) {
			return nil, fmt.Errorf("%w: contradictory cidr", ErrInvalidPolicy)
		}
	}
	compiled.CanonicalJSON, err = json.Marshal(compiled.Policy)
	if err != nil {
		return nil, fmt.Errorf("%w: canonical json", ErrInvalidPolicy)
	}
	digest := sha256.Sum256(compiled.CanonicalJSON)
	compiled.Hash = hex.EncodeToString(digest[:])
	return compiled, nil
}

func (policy *CompiledPolicy) Evaluate(protocol Protocol, source netip.Addr, target Target, management []netip.Prefix) error {
	if !contains(policy.Policy.Protocols, protocol) {
		return ErrProtocolDenied
	}
	if len(policy.source) > 0 && !prefixContains(policy.source, source.Unmap()) {
		return ErrSourceDenied
	}
	if target.Port == 0 {
		return ErrInvalidTarget
	}
	if len(policy.Policy.AllowedPorts) > 0 && !portAllowed(policy.Policy.AllowedPorts, target.Port) {
		return ErrPortDenied
	}
	host := strings.TrimSpace(target.Host)
	if host == "" {
		return ErrInvalidTarget
	}
	addresses := append([]netip.Addr(nil), target.Resolved...)
	numeric, numericErr := netip.ParseAddr(strings.Trim(host, "[]"))
	canonicalHost := ""
	if numericErr == nil {
		addresses = append(addresses, numeric.Unmap())
	} else {
		var err error
		canonicalHost, err = canonicalDomain(host)
		if err != nil {
			return ErrInvalidTarget
		}
	}
	if len(addresses) == 0 && policy.Policy.DNSMode == DNSGateway {
		return ErrDNSNoAnswers
	}
	for _, address := range addresses {
		address = address.Unmap()
		if PlatformDenied(address, management) {
			return ErrPlatformDenied
		}
	}
	if prefixContainsAny(policy.denyCIDRs, addresses) || domainMatchesAny(policy.Policy.TargetDenyDomains, canonicalHost) {
		return ErrExplicitDenied
	}
	hasAllowlist := len(policy.allowCIDRs) > 0 || len(policy.Policy.TargetAllowDomains) > 0
	allowedByDomain := domainMatchesAny(policy.Policy.TargetAllowDomains, canonicalHost)
	allowedByCIDR := len(addresses) > 0
	for _, address := range addresses {
		allowedByCIDR = allowedByCIDR && prefixContains(policy.allowCIDRs, address.Unmap())
	}
	if hasAllowlist && !allowedByDomain && !allowedByCIDR {
		return ErrTargetNotAllowed
	}
	return nil
}

func ValidateDNSAnswers(answers []netip.Addr, management []netip.Prefix) error {
	if len(answers) == 0 {
		return ErrDNSNoAnswers
	}
	for _, address := range answers {
		if PlatformDenied(address.Unmap(), management) {
			return ErrPlatformDenied
		}
	}
	return nil
}

func PlatformDenied(address netip.Addr, management []netip.Prefix) bool {
	if !address.IsValid() || address.IsUnspecified() || address.IsLoopback() || address.IsMulticast() || address.IsLinkLocalUnicast() || address.IsPrivate() {
		return true
	}
	for _, prefix := range management {
		if prefix.Contains(address) {
			return true
		}
	}
	if address.Is4() {
		return inAny(address, "0.0.0.0/8", "100.64.0.0/10", "169.254.0.0/16", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4")
	}
	return inAny(address, "fc00::/7", "fe80::/10", "fec0::/10", "2001:db8::/32")
}

func normalizeCIDRs(values []string) ([]string, []netip.Prefix, error) {
	set := map[string]netip.Prefix{}
	for _, value := range values {
		prefix, err := netip.ParsePrefix(strings.TrimSpace(value))
		if err != nil {
			return nil, nil, fmt.Errorf("%w: invalid cidr", ErrInvalidPolicy)
		}
		prefix = prefix.Masked()
		set[prefix.String()] = prefix
	}
	keys := make([]string, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	prefixes := make([]netip.Prefix, 0, len(keys))
	for _, key := range keys {
		prefixes = append(prefixes, set[key])
	}
	return keys, prefixes, nil
}

func normalizeDomains(values []string) ([]string, error) {
	set := map[string]bool{}
	for _, value := range values {
		canonical, err := canonicalDomain(value)
		if err != nil {
			return nil, err
		}
		set[canonical] = true
	}
	result := make([]string, 0, len(set))
	for value := range set {
		result = append(result, value)
	}
	sort.Strings(result)
	return result, nil
}

func canonicalDomain(value string) (string, error) {
	value = strings.TrimSuffix(strings.TrimSpace(value), ".")
	if value == "" || strings.Contains(value, "*") {
		return "", fmt.Errorf("%w: invalid domain", ErrInvalidPolicy)
	}
	result, err := idna.Lookup.ToASCII(value)
	if err != nil {
		return "", fmt.Errorf("%w: invalid domain", ErrInvalidPolicy)
	}
	result = strings.ToLower(result)
	if len(result) > 253 {
		return "", fmt.Errorf("%w: domain too long", ErrInvalidPolicy)
	}
	for _, label := range strings.Split(result, ".") {
		if label == "" || len(label) > 63 || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
			return "", fmt.Errorf("%w: invalid domain", ErrInvalidPolicy)
		}
	}
	return result, nil
}

func normalizePorts(values []PortRange) ([]PortRange, error) {
	for _, value := range values {
		if value.From == 0 || value.To == 0 || value.From > value.To {
			return nil, fmt.Errorf("%w: invalid port range", ErrInvalidPolicy)
		}
	}
	sort.Slice(values, func(i, j int) bool {
		if values[i].From == values[j].From {
			return values[i].To < values[j].To
		}
		return values[i].From < values[j].From
	})
	result := make([]PortRange, 0, len(values))
	for _, value := range values {
		if len(result) > 0 && uint32(value.From) <= uint32(result[len(result)-1].To)+1 {
			if value.To > result[len(result)-1].To {
				result[len(result)-1].To = value.To
			}
			continue
		}
		result = append(result, value)
	}
	return result, nil
}

func validateLimits(limits Limits) error {
	if limits.MaxConnections == 0 || limits.MaxConnections > 100000 || limits.MaxConnectionRate == 0 || limits.MaxConnectionRate > 100000 || limits.IdleTimeoutSeconds == 0 || limits.IdleTimeoutSeconds > 86400 {
		return fmt.Errorf("%w: invalid limit", ErrInvalidPolicy)
	}
	if (limits.TrafficWindowSeconds == 0) != (limits.MaxWindowBytes == 0) || limits.TrafficWindowSeconds > 86400 {
		return fmt.Errorf("%w: invalid traffic window", ErrInvalidPolicy)
	}
	return nil
}

func domainMatchesAny(rules []string, host string) bool {
	for _, rule := range rules {
		if host == rule || strings.HasSuffix(host, "."+rule) {
			return true
		}
	}
	return false
}
func prefixContains(prefixes []netip.Prefix, address netip.Addr) bool {
	for _, prefix := range prefixes {
		if prefix.Contains(address) {
			return true
		}
	}
	return false
}
func prefixContainsAny(prefixes []netip.Prefix, addresses []netip.Addr) bool {
	for _, address := range addresses {
		if prefixContains(prefixes, address.Unmap()) {
			return true
		}
	}
	return false
}
func portAllowed(ranges []PortRange, port uint16) bool {
	for _, r := range ranges {
		if port >= r.From && port <= r.To {
			return true
		}
	}
	return false
}
func contains[T comparable](values []T, expected T) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
func inAny(address netip.Addr, values ...string) bool {
	for _, value := range values {
		if netip.MustParsePrefix(value).Contains(address) {
			return true
		}
	}
	return false
}
