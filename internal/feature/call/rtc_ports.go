package call

import (
	"fmt"
	"log/slog"

	"github.com/ygelfand/echolocal/internal/android/firewall"
)

// Card 14: pion's ICE listeners bind OS-ephemeral UDP ports (32768..60999),
// OUTSIDE the Sendspin UDP window (16384..32767) the dot's DROP firewall
// accepts. Upstream libcountertop pkg/media/rtc/rtc.go:api() assembles its
// webrtc.SettingEngine privately with no seam to call SetEphemeralUDPPorts,
// so the fork-side fix is the firewall route: a dedicated ECHOLOCAL_CALLUDP
// chain accepting the ephemeral UDP range on wlan0, armed by the call
// feature itself. Trade-off: this is a wider exposure than a constrained
// SettingEngine would be, but it is protocol-scoped (UDP only), interface-
// scoped (wlan0), and its own chain (never flushes a shared one — card-11
// lesson). Sendspin's existing window and rules are untouched.
const (
	icePortLow  = 32768 // Linux net.ipv4.ip_local_port_range default start
	icePortHigh = 60999
)

// armICEPorts opens the ephemeral UDP window the pion ICE agent binds its
// listeners on. Idempotent: OpenUDPRange flushes only the dedicated chain.
func armICEPorts() {
	if err := firewall.OpenUDPRange(firewall.CallUDP, icePortLow, icePortHigh); err != nil {
		slog.Error("opening the call ICE UDP range failed", "err", err)
		return
	}
	slog.Info("armed call ICE UDP range", "chain", firewall.CallUDP,
		"ports", fmt.Sprintf("%d:%d", icePortLow, icePortHigh))
}