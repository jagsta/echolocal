package voice

import (
	"log/slog"
	"sync"

	esphome "github.com/ygelfand/go-esphome-device"

	"github.com/ygelfand/libcountertop/pkg/assistant/satellite"
	sharedcomponent "github.com/ygelfand/libcountertop/pkg/runtime/component"

	"github.com/ygelfand/echolocal/internal/component"
	"github.com/ygelfand/echolocal/internal/config"
	"github.com/ygelfand/echolocal/internal/feature/media"
	"github.com/ygelfand/echolocal/internal/feature/timer"
	"github.com/ygelfand/echolocal/internal/feature/wakeword"
	"github.com/ygelfand/echolocal/internal/hardware/buttons"
	"github.com/ygelfand/echolocal/internal/hardware/speaker"
	"github.com/ygelfand/echolocal/internal/lib/wake"
)

func init() {
	component.Register(sharedcomponent.Device, Get, sharedcomponent.Order(30))
}

const Features = satellite.Features

type Voice struct {
	*satellite.Satellite

	vs *esphome.VoiceSatellite
}

var (
	once   sync.Once
	shared *Voice
)

// guard is consulted before any assistant turn may start (wake or button).
// The drop-in intercom session layer installs it (D-DI-5 wake-word
// suppression, INTERCEPT POINT B). nil means no guard is installed.
var guard func() bool

// SetGuard installs the pre-turn arbitration check. Called once by
// internal/feature/intercom wiring; checked in Start (INTERCEPT POINT A
// backstop for the wake path — detect.PauseWhen is the primary gate there)
// and before Action button turns.
func SetGuard(f func() bool) { guard = f }

func blocked() bool { return guard != nil && guard() }

func Get() *Voice {
	once.Do(func() { shared = build() })
	return shared
}

func build() *Voice {
	ours := wake.Lib().Ours()
	slots := wanted(ours, wakeword.Slots)

	vs := &esphome.VoiceSatellite{OnExternalWakeWords: wake.Lib().Answer}
	v := &Voice{
		Satellite: satellite.New(
			vs,
			dependencies(),
			satellite.Options{
				Slots:   wakeword.Slots,
				Initial: slots,
				Timers:  timer.Get(),
				Sound:   speaker.Sound(),
				Player:  media.Get(),
				Save:    func(slot int, id string) error { return config.Set().Wake(slot).ID(id) },
			},
		),
		vs: vs,
	}
	slog.Info("wake words", "ours", len(ours), "slots", slots)

	wakeword.Requested.Listen(v.Start)

	buttons.Get().Events.Listen(func(e buttons.Event) {
		if e.Name != buttons.Action {
			return
		}
		switch e.Kind {
		case buttons.Tap:
			if blocked() {
				slog.Debug("action tap suppressed: intercom session owns the turn")
				return
			}
			v.Action()
		case buttons.Hold:
			v.ActionHold()
		}
	})

	return v
}

// Start overrides satellite.Satellite.Start to apply the intercom pre-turn
// guard (INTERCEPT POINT A backstop: the wake path's primary gate is
// detect.PauseWhen, installed by the intercom wiring; this catches anything
// that still reaches a turn start while a drop-in call owns the device's
// single turn slot — D-DI-5 suppression is deliberate policy, with
// go-esphome-device's ErrTurnInProgress as the final accident backstop).
func (v *Voice) Start(slot int) {
	if blocked() {
		slog.Debug("wake suppressed: intercom session active", "slot", slot+1)
		return
	}
	v.Satellite.Start(slot)
}

func (v *Voice) ActionHold() { v.Start(1) }

// VS exposes the underlying protocol satellite. The drop-in intercom session
// layer uses it to hold the perpetual per-call turn (StartTurn/SendAudio/
// EndAudio) and to keep wake-word turns refused while a call owns the device's
// single turn slot (D-DI-5 suppression — see internal/feature/intercom).
func (v *Voice) VS() *esphome.VoiceSatellite { return v.vs }
