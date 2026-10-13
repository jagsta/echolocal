package intercom

// Card 7 (D-DI-8, specs/phone-dropin-webrtc.md): the phone pairing key as a
// ctl-settings row on the "intercom" domain. First run sets it via
// `ctl settings intercom.phone_key <key>`; empty (the default) keeps phone
// sources fail-closed OFF in the call package's pairing gate.
//
// This feature's own Get()/shared/once for the session Intercom live in
// wiring.go, so the pairing knob singleton is kept in package-level state
// here with distinct names.

import (
	"log/slog"
	"sync"

	setting "github.com/ygelfand/libcountertop/pkg/settings"

	"github.com/ygelfand/echolocal/internal/component"
	"github.com/ygelfand/echolocal/internal/config"
)

const Group setting.Group = "Intercom"

type Setting = setting.Setting[config.Intercom]

type Pairing struct {
	knobs *setting.Controls[config.Intercom]
}

var pairingOnce sync.Once
var pairingShared *Pairing

func PairingSettings() *Pairing {
	pairingOnce.Do(func() {
		pairingShared = &Pairing{knobs: &setting.Controls[config.Intercom]{
			Table:  PairingTable(),
			Device: component.DeviceAssistant,
			Read:   func() config.Intercom { return config.Get().Intercom },
			Save:   store,
		}}
		component.Settings.Add(pairingShared.knobs)
	})
	return pairingShared
}

// Name satisfies the runtime component.Component contract.
func (p *Pairing) Name() string { return "intercom pairing setting" }

func PairingTable() *setting.Table[config.Intercom] {
	return setting.NewTable("intercom", []setting.Group{Group}, []Setting{
		setting.Words(
			setting.Setting[config.Intercom]{
				Name: "phone_key", Group: Group, Kind: setting.Secret, Icon: "mdi:key-wireless",
			},
			128,
			func(c *config.Intercom) *string { return &c.PhoneKey },
		),
	}, setting.Messages{})
}

func store(s Setting, v string) error {
	if err := setting.Store(config.IntercomSection, s, v); err != nil {
		return err
	}
	slog.Info("saved", "setting", "intercom."+s.Name)
	return nil
}
