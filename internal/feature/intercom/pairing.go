package intercom

// Card 7 (D-DI-8, specs/phone-dropin-webrtc.md): the phone pairing key as a
// setting row serializes the phone_keys map (D-DI-9b, "name:key" pairs) so
// the ctl-settings row stays a Text field; first run sets it via
// `ctl settings intercom.phone_keys "jag:S3cret,hannah:L4ter"`; empty string
// (the default) keeps phone sources fail-closed OFF in the call package's
// pairing gate.
//
// This feature's own Get()/shared/once for the session Intercom live in
// wiring.go, so the pairing knob singleton is kept in package-level state
// here with distinct names.

import (
	"log/slog"
	"strings"
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
		setting.Setting[config.Intercom]{
			Name: "phone_keys", Group: Group, Kind: setting.Secret, Icon: "mdi:key-wireless",
			Read: func(c *config.Intercom) string { return config.PhoneKeysText(c.PhoneKeys) },
			Write: func(c *config.Intercom, v string) error {
				keys, err := config.ParsePhoneKeysText(v)
				if err != nil {
					return err
				}
				c.PhoneKeys = keys
				return nil
			},
		},
		// Card 15: cors_origins allowlist as a plain Text row (NOT secret —
		// card 12's D-DI-9 field); empty string clears to nil (fail-closed),
		// malformed entries are rejected at SET time.
		setting.Setting[config.Intercom]{
			Name: "cors_origins", Group: Group, Kind: setting.Text, Icon: "mdi:shield-link-variant",
			Read: func(c *config.Intercom) string {
				return strings.Join(c.CORSOrigins, ",")
			},
			Write: func(c *config.Intercom, v string) error {
				origins, err := config.ParseCORSOriginsText(v)
				if err != nil {
					return err
				}
				c.CORSOrigins = origins
				return nil
			},
		},
	}, setting.Messages{})
}

func store(s Setting, v string) error {
	if err := setting.Store(config.IntercomSection, s, v); err != nil {
		return err
	}
	slog.Info("saved", "setting", "intercom."+s.Name)
	return nil
}
