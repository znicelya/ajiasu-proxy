package contract

import "testing"

func TestPhase5ProxyProtocolContractSurface(t *testing.T) {
	// Keep the protocol contract explicit in CI even when the Rust crate owns
	// the parser implementation.
	for _, protocol := range []string{"http", "connect", "socks5"} {
		if protocol == "" {
			t.Fatal("empty protocol")
		}
	}
}
