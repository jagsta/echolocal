package intercom

// Wiring binds the drop-in intercom session machine to echod's live seams.
// Everything that touches hardware or singletons lives here; session.go stays
// pure state and timing so its rules are unit-testable without hardware.
//
// Seams used (libcountertop wiring pattern of internal/feature/call/):
//   - mic capture: a NEW dedicated listener on the existing fan-out —
//     mic.Get().Listen("intercom") runs alongside "call"/"turn", so wake
//     detection and calls never starve each other (D-DI-4 v1: frames flow
//     device→HA as VoiceAssistantAudio via VS.SendAudio).
//   - speaker playback: config.StreamIntercom, the NEW mixer lane added in
//     internal/config/volume.go — the peer's VoiceAssistantAudio PCM (little
//     -endian 16kHz mono, same encoding the satellite's reply stream decodes)
//     is played on it so call volume is independently adjustable.
//   - turn lifecycle: voice.VS() StartTurn/SendAudio/EndAudio hold ONE
//     perpetual turn per call (probe finding; no go-esphome-device changes).
//   - state exposure: a TextSensor on the microphone sub-device so HA sees
//     idle | ringing | in-call | muted | joined.
//   - chimes/LED: ring chime + Beacon, connect chime + Pulse, end chime,
//     amber Paint while muted (D-DI-6), all through led.Get().Claim.

import (
	"context"
	"encoding/binary"
	"log/slog"
	"sync"
	"time"

	esphome "github.com/ygelfand/go-esphome-device"

	sharedtone "github.com/ygelfand/libcountertop/pkg/audio/tone"
	sharedcomponent "github.com/ygelfand/libcountertop/pkg/runtime/component"

	"github.com/ygelfand/echolocal/internal/component"
	"github.com/ygelfand/echolocal/internal/config"
	"github.com/ygelfand/echolocal/internal/feature/detect"
	"github.com/ygelfand/echolocal/internal/feature/voice"
	"github.com/ygelfand/echolocal/internal/hardware/buttons"
	"github.com/ygelfand/echolocal/internal/hardware/led"
	"github.com/ygelfand/echolocal/internal/hardware/mic"
	"github.com/ygelfand/echolocal/internal/hardware/speaker"
)

func init() {
	// Order 61: after voice (30) and detect (40) so their seams exist, after
	// the webrtc call ring (21) but on the Device phase like them.
	component.Register(sharedcomponent.Device, Get, sharedcomponent.Order(61))
}

type Intercom struct {
	session *Session
	vs      *esphome.VoiceSatellite
	led     *led.Claim
	status  *esphome.TextSensor

	unlistenMic func()

	mu      sync.Mutex
	muted   bool // physical mic transmit off in-call (D-DI-6)
	streams []<-chan []int16
}

var (
	once   sync.Once
	shared *Intercom
)

func Get() *Intercom {
	once.Do(func() {
		shared = &Intercom{
			session: New(time.Duration(config.Get().Intercom.MaxMinutes) * time.Minute),
			led:     led.Get().Claim(led.PriorityAlarm),
			status: &esphome.TextSensor{
				Base: esphome.Base{
					ObjectID: "intercom_session",
					DeviceID: component.DeviceMicrophone,
					Name:     "Intercom session",
					Icon:     "mdi:phone-in-talk",
				},
			},
		}
		shared.wire()
	})
	return shared
}

func (i *Intercom) Name() string { return "drop-in intercom" }

// Entities exposes the session state so HA can see it (ESPHome native API).
func (i *Intercom) Entities() []esphome.Entity { return []esphome.Entity{i.status} }

// wire installs every subscription exactly once at startup.
func (i *Intercom) wire() {
	vs := voice.Get().VS()

	s := i.session

	// INTERCEPT POINT A (primary, wake path): wake detection paused entirely
	// while the session is off idle — the detect engine's OnDetect never fires
	// a slot, so no wake StartTurn is even attempted (D-DI-5).
	detect.PauseWhen(func() bool { return s.Busy() })

	// INTERCEPT POINT B (button + belt): voice.Start and voice action taps
	// check this guard before any turn starts.
	voice.SetGuard(s.Busy)

	// HA-visible session state.
	s.Changed = append(s.Changed, func(from, to State) {
		i.status.Set(string(to))
		i.show(to)
	})

	// Ring: chime now (ring floor is enforced by the machine, D-DI-3).
	s.Changed = append(s.Changed, func(from, to State) {
		switch to {
		case Ringing:
			i.chime("intercom_ring", sharedtone.ToneTimer)
		case InCall:
			i.chime("intercom_connect", sharedtone.ToneUnmute)
		case Idle:
			if from != Idle {
				i.chime("intercom_end", sharedtone.ToneMute)
			}
		}
	})
	s.Ended = append(s.Ended, func(reason string) {
		slog.Info("intercom call self-ended", "reason", reason)
	})

	// Buttons: action = accept while ringing, mute toggle in-call,
	// volume-down ends (mirrors call.go's mapping).
	buttons.Get().Take(i.take)

	// Mic capture: ONE listener on the existing fan-out, alongside "turn" and
	// "call" (the wiring-only part of the D-DI-4 v1 data path).
	frames, unlisten := mic.Get().Listen("intercom")
	i.unlistenMic = unlisten
	go i.pump(frames, vs)

	// Peer audio → the new StreamIntercom mixer lane. The satellite's
	// OnTTSAudio is already owned by the assistant conversation, so the relay
	// (card 3, HA side) will push decoded PCM here through PlayPeer; keep the
	// speaker hook ready rather than stealing the callback.
	i.status.Set(string(Idle))
}

// take implements the in-call button contract (D-DI-3 accept, D-DI-6 mute,
// hang up). Returns true when the event was consumed.
func (i *Intercom) take(e buttons.Event) bool {
	if e.Kind != buttons.Tap {
		return false
	}
	s := i.session
	switch s.Now() {
	case Idle:
		return false
	case Ringing:
		switch e.Name {
		case buttons.Action:
			i.Accept()
			return true
		case buttons.VolumeDown:
			s.End("hangup")
			return true
		}
		return false
	default: // in-call (incl. muted/joined)
		switch e.Name {
		case buttons.Action:
			i.ToggleMute()
			return true
		case buttons.VolumeDown:
			s.End("hangup")
			return true
		}
		return false
	}
}

// Accept connects when the ring floor has elapsed — the device refuses an
// early connect regardless of what the network asks (D-DI-3) — then opens the
// perpetual turn, which is what makes wake-word turns structurally impossible.
func (i *Intercom) Accept() {
	s := i.session
	state, ok := s.Accept()
	if !ok {
		slog.Warn("intercom connect refused", "state", state)
		i.chime("intercom_refused", sharedtone.ToneCancel)
		return
	}
	if err := i.vs.StartTurn("", nil); err != nil {
		slog.Error("intercom turn open failed", "err", err)
		s.End("dropped")
	}
}

// ToggleMute flips mic transmit off/on in-call (D-DI-6); mute keeps the turn
// open but stops pumping frames.
func (i *Intercom) ToggleMute() {
	i.mu.Lock()
	i.muted = !i.muted
	muted := i.muted
	i.mu.Unlock()
	i.session.Mute(muted)
	slog.Info("intercom mic", "muted", muted)
}

// show maps state onto the existing LED conventions (D-DI-5): ring pulse →
// Beacon, in-call solid → Pulse, muted → static amber paint, end → clear.
func (i *Intercom) show(to State) {
	if i.led == nil {
		return
	}
	c := config.Get().Intercom
	switch to {
	case Ringing:
		i.led.Play(effectOr(c.Ring, "Beacon"), led.Color{})
	case InCall, Joined:
		i.led.Play(effectOr(c.Call, "Pulse"), led.Color{})
	case Muted:
		i.led.Paint([]led.Color{amber})
	default:
		i.led.Clear()
	}
}

var amber = led.Color{R: 0xff, G: 0x80, B: 0x00}

func effectOr(name, fallback string) string {
	if name == "" {
		return fallback
	}
	return name
}

func (i *Intercom) chime(name string, notes []sharedtone.Note) {
	speaker.Sound().Interject(func(p *speaker.Player) {
		p.Chime(config.StreamFeedback, sharedtone.Level, notes...)
	})
	slog.Debug("intercom chime", "name", name)
}

// pump streams captured mic frames into the open call turn. Muted or idle,
// frames are drained and discarded so the listener never backs up (the
// fan-out keeps every frame for slow listeners otherwise).
func (i *Intercom) pump(frames <-chan []int16, vs *esphome.VoiceSatellite) {
	for frame := range frames {
		i.mu.Lock()
		muted := i.muted
		i.mu.Unlock()
		if muted || i.session.Now() == Idle {
			continue
		}
		buf := make([]byte, len(frame)*2)
		for n, sample := range frame {
			binary.LittleEndian.PutUint16(buf[n*2:], uint16(sample))
		}
		if err := vs.SendAudio(buf); err != nil {
			slog.Debug("intercom send failed", "err", err)
		}
	}
}

// PlayPeer plays relayed call audio on the intercom mixer lane. The HA-side
// relay (card 3) calls this with the peer's VoiceAssistantAudio PCM payload.
func (i *Intercom) PlayPeer(pcm []byte) {
	samples := make([]int16, len(pcm)/2)
	for n := range samples {
		samples[n] = int16(binary.LittleEndian.Uint16(pcm[n*2:]))
	}
	speaker.Get().PlayStream(config.StreamIntercom, samples)
}

// Session exposes the machine for the web/ctl surfaces and tests.
func (i *Intercom) Session() *Session { return i.session }

// Start keeps the wiring a no-op component starter; capture already runs.
func (i *Intercom) Start(context.Context) error { return nil }