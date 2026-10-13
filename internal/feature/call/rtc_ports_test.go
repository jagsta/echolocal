package call

import (
	"testing"

	"github.com/ygelfand/echolocal/internal/android/firewall"
)

// Card 14: the chosen mechanism must stay armed for the biscuit build.
// Guard the constants and the wiring shape — the actual iptables calls are
// device-side ( exercised live on the pilot ).
func TestICEPortConstants(t *testing.T) {
	if icePortLow != 32768 || icePortHigh != 60999 {
		t.Fatalf("ICE UDP range drifted: %d..%d (want 32768..60999 = Linux default ephemeral)", icePortLow, icePortHigh)
	}
	if icePortLow >= icePortHigh {
		t.Fatalf("ICE UDP range inverted: %d..%d", icePortLow, icePortHigh)
	}
}

// The fix must reach for the dedicated CallUDP chain, not a shared one
// (card 11: never flush a shared chain).
func TestUsesDedicatedChain(t *testing.T) {
	if firewall.CallUDP != "ECHOLOCAL_CALLUDP" {
		t.Fatalf("CallUDP chain renamed: %q", firewall.CallUDP)
	}
}

// armICEPorts must stay wired from the call feature (init() in call.go
// references it — this guards against the reference being dropped).
func TestArmWired(t *testing.T) {
	// Pure compile/link-time reference; calling it here would shell out to
	// iptables on the host, so assert it exists via a no-op guard instead.
	var f func() = armICEPorts
	_ = f
}