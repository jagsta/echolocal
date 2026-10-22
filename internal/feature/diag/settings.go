package diag

// Card E: remote_adb as a ctl-settings row alongside the ESPHome switch.
// The control-plane story must match the reality the switch enforces: the
// ESPHome switch (remote() below) is the device's own UI, this row is the
// host-side ctl surface. Both route the same change through d.reachable —
// the honest toggle that owns the firewall chain AND adbd's tcp mode.
//
// The setting-table lesson from card 15 is honored: a config field used by a
// ctl row must land WITH its table row in the same change (the row IS what
// `ctl settings` resolves against; config alone errors with "no such
// setting").

import (
	"log/slog"
	"sync"

	setting "github.com/ygelfand/libcountertop/pkg/settings"

	"github.com/ygelfand/echolocal/internal/component"
	"github.com/ygelfand/echolocal/internal/config"
	"github.com/ygelfand/echolocal/internal/labels"
)

const Group setting.Group = "Diagnostics"

type Setting = setting.Setting[config.Diag]

// Table describes the diag settings row set.
func Table() *setting.Table[config.Diag] {
	return setting.NewTable("diag", []setting.Group{Group}, []Setting{
		setting.Flag(
			Setting{Name: "remote_adb", Group: Group, Icon: "mdi:bug-outline"},
			func(c *config.Diag) *bool { return &c.ADB },
		),
	}, labels.Messages()).Local()
}

// Settings registers the host-side ctl surface for the diag group.
type Settings struct {
	knobs *setting.Controls[config.Diag]
}

var (
	settingsOnce   sync.Once
	settingsShared *Settings
)

// SettingsShared registers the host-side ctl surface for the diag group.
func SettingsShared() *Settings {
	settingsOnce.Do(func() {
		settingsShared = &Settings{knobs: &setting.Controls[config.Diag]{
			Table:  Table(),
			Device: component.DeviceAssistant,
			Read:   func() config.Diag { return config.Get().Diag },
			Save:   store,
		}}
		component.Settings.Add(settingsShared.knobs)
	})
	return settingsShared
}

func store(s Setting, v string) error {
	at := config.Get().Diag
	if err := s.Write(&at, v); err != nil {
		return err
	}
	// Route the change through the same path the ESPHome switch uses so the
	// firewall chain and adbd's tcp mode stay the single source of truth.
	Get().SetADB(at.ADB)
	if err := config.Set().Diag().ADB(at.ADB); err != nil {
		return err
	}
	slog.Info("saved", "setting", "diag."+s.Name, "using", at.ADB)
	return nil
}

func (s *Settings) Name() string { return "diagnostics settings" }
