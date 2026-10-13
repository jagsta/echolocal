//go:build board_biscuit

package all

import (
	"slices"
	"testing"

	harness "github.com/ygelfand/libcountertop/pkg/runtime/control"

	"github.com/ygelfand/echolocal/internal/component"
	"github.com/ygelfand/echolocal/internal/config"
)

// Card 10 (D-DI-8): linking internal/feature/intercom from this package puts
// its component.Register calls (wiring.go init) into every echod build, so
// the card-8 pairing settings row must show up in the ctl settings registry —
// `ctl settings intercom.phone_keys` — instead of the direct state.json write
// the pilot needed.
func TestIntercomPairingSettingsRowRegistered(t *testing.T) {
	var names []string
	for _, row := range harness.Registered[config.Config](&component.Settings) {
		names = append(names, row.Name)
	}
	if !slices.Contains(names, "intercom.phone_keys") {
		t.Errorf("intercom.phone_keys not in the ctl settings registry; have: %v", names)
	}
}
