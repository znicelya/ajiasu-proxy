package proxyaccess

import (
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"testing"
)

func TestCompilePolicyCanonicalizesDeterministically(t *testing.T) {
	raw := []byte(`{"protocols":["socks5","http","http"],"dns_mode":"gateway","target_allow_domains":["BÜCHER.Example."],"allowed_ports":[{"from":443,"to":443},{"from":80,"to":80}],"limits":{"max_connections":10,"max_connection_rate":5,"idle_timeout_seconds":30,"max_bytes_per_connection":1000,"traffic_window_seconds":60,"max_window_bytes":10000}}`)
	first, err := CompilePolicyJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	second, err := CompilePolicyJSON(first.CanonicalJSON)
	if err != nil {
		t.Fatal(err)
	}
	if first.Hash != second.Hash || string(first.CanonicalJSON) != string(second.CanonicalJSON) {
		t.Fatalf("canonical policy is not stable")
	}
	if first.Policy.TargetAllowDomains[0] != "xn--bcher-kva.example" {
		t.Fatalf("domain=%q", first.Policy.TargetAllowDomains[0])
	}
}

func TestPolicyGoldenVectors(t *testing.T) {
	data, err := os.ReadFile("../../tests/fixtures/phase5/policy_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var vectors []struct {
		Name      string          `json:"name"`
		Input     json.RawMessage `json:"input"`
		Canonical string          `json:"canonical"`
		SHA256    string          `json:"sha256"`
	}
	if err := json.Unmarshal(data, &vectors); err != nil {
		t.Fatal(err)
	}
	for _, vector := range vectors {
		t.Run(vector.Name, func(t *testing.T) {
			compiled, err := CompilePolicyJSON(vector.Input)
			if err != nil {
				t.Fatal(err)
			}
			if string(compiled.CanonicalJSON) != vector.Canonical {
				t.Fatalf("canonical=%s", compiled.CanonicalJSON)
			}
			if compiled.Hash != vector.SHA256 {
				t.Fatalf("hash=%s", compiled.Hash)
			}
		})
	}
}

func TestPolicyEvaluationPrecedenceAndDNSRebinding(t *testing.T) {
	policy, err := CompilePolicyJSON([]byte(`{"protocols":["connect"],"dns_mode":"gateway","source_cidrs":["203.0.113.0/24"],"target_allow_domains":["example.com"],"target_deny_cidrs":["8.8.4.0/24"],"allowed_ports":[{"from":443,"to":443}],"limits":{"max_connections":10,"max_connection_rate":5,"idle_timeout_seconds":30,"max_bytes_per_connection":0,"traffic_window_seconds":0,"max_window_bytes":0}}`))
	if err != nil {
		t.Fatal(err)
	}
	target := Target{Host: "api.example.com", Port: 443, Resolved: []netip.Addr{netip.MustParseAddr("8.8.8.8")}}
	if err := policy.Evaluate(ProtocolCONNECT, netip.MustParseAddr("203.0.113.9"), target, nil); err != nil {
		t.Fatal(err)
	}
	target.Resolved = append(target.Resolved, netip.MustParseAddr("169.254.169.254"))
	if err := policy.Evaluate(ProtocolCONNECT, netip.MustParseAddr("203.0.113.9"), target, nil); !errors.Is(err, ErrPlatformDenied) {
		t.Fatalf("error=%v", err)
	}
}

func TestPolicyRejectsWildcardsContradictionsAndUnsafeNumericTargets(t *testing.T) {
	if _, err := CompilePolicyJSON([]byte(`{"target_allow_domains":["*.example.com"]}`)); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("error=%v", err)
	}
	if _, err := CompilePolicyJSON([]byte(`{"target_allow_cidrs":["8.8.8.0/24"],"target_deny_cidrs":["8.8.8.0/24"]}`)); !errors.Is(err, ErrInvalidPolicy) {
		t.Fatalf("error=%v", err)
	}
	policy, err := CompilePolicy(DefaultPolicy())
	if err != nil {
		t.Fatal(err)
	}
	if err := policy.Evaluate(ProtocolHTTP, netip.MustParseAddr("8.8.8.8"), Target{Host: "127.0.0.1", Port: 80}, nil); !errors.Is(err, ErrPlatformDenied) {
		t.Fatalf("error=%v", err)
	}
}
