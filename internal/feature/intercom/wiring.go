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
//     internal/config/volume.go — the peer's call audio (little-endian 16kHz
//     mono: the dot↔dot leg's decoded Opus frame, or the satellite reply
//     stream's encoding) is played on it so call volume is independently
//     adjustable. The lane is consumed as 48kHz stereo interleaved int16, so
//     PlayMono converts each frame first (D-P2P-6 correction, 15 Oct).
//   - turn lifecycle: voice.VS() StartTurn/SendAudio/EndAudio hold ONE
//     perpetual turn per call (probe finding; no go-esphome-device changes).
//   - state exposure: a TextSensor on the microphone sub-device so HA sees
//     idle | ringing | in-call | muted | joined.
//   - chimes/LED: ring chime + Beacon, connect chime + Pulse, end chime,
//     amber Paint while muted (D-DI-6), all through led.Get().Claim.

import (
	"context"
	"encoding/binary"
	esphome "github.com/ygelfand/go-esphome-device"
	"log/slog"
	"sync"
	"time"

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
	// Card 7 (D-DI-8): the phone pairing-key setting row; registering it from
	// this init pulls it into the ctl settings registry on every build.
	component.Register(sharedcomponent.Device, PairingSettings, sharedcomponent.Order(63))
}

type Intercom struct {
	session *Session
	vs      *esphome.VoiceSatellite
	led     *led.Claim
	status  *esphome.TextSensor

	unlistenMic func()

	mu       sync.Mutex
	muted    bool // physical mic transmit off in-call (D-DI-6)
	turnOpen bool

	// ringChime repeats the ring chime every ring period while the session
	// sits in Ringing (15 Oct defect: the tone sounded only ONCE on the
	// Idle→Ringing transition, later rings were visual-only). ringTimer is
	// swappable for tests; nil means use a real time.Ticker.
	ringTimer func(d time.Duration, fire func()) (stop func())
	ringStop  func()

	// transport selects where call audio travels (D-P2P-7): "p2p" (default,
	// the direct leg) or "harelays" (the mothballed HA relay). It gates the
	// mic pump's fallback to the ESPHome voice turn — post-P2P no call audio
	// may ride a voice turn, which is the assistant-hijack failure mode that
	// killed the v1 relay (D-P2P-8).
	transport string

	// leg is the live direct media leg (D-P2P-4). Non-nil means the direct
	// path owns call audio; teardown closes it on every transition to Idle.
	leg mediaLeg

	// turnOps is swappable so tests can release the turn without a live
	// satellite; nil wires the real VoiceSatellite EndAudio/StopTurn.
	turnOps func()

	// play is the inbound-audio sink (config.StreamIntercom in production);
	// post is the offer POST used by the caller side. Both are injectable so
	// the leg can be exercised without hardware or a TLS listener.
	play func([]int16)
	post func(ctx context.Context, peer string, m dotOfferMsg) (string, error)

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
	i.vs = vs // shared satellite for Accept (StartTurn) and releaseTurn

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

	// Ring: chime now (ring floor is enforced by the machine, D-DI-3) and
	// REPEAT it every ring period until the session leaves Ringing — answer,
	// hangup, or ring timeout. The 15 Oct live defect was the tone sounding
	// only on the first ring; the Idle→Ringing chime is one-shot, so a
	// repeating timer owns the audible ring and any transition out of
	// Ringing stops it.
	s.Changed = append(s.Changed, func(from, to State) {
		switch to {
		case Ringing:
			i.stopRing() // a re-ring while ringing restarts the pattern
			if i.ringTimer == nil {
				i.ringTimer = repeatingTimer
			}
			i.ringStop = i.ringTimer(ringChimePeriod, func() {
				if i.session.Now() == Ringing {
					i.chime("intercom_ring", sharedtone.ToneTimer)
				}
			})
		default:
			i.stopRing()
		}
		if to == Ringing {
			i.chime("intercom_ring", sharedtone.ToneTimer)
		} else if to == InCall {
			i.chime("intercom_connect", sharedtone.ToneUnmute)
		} else if to == Idle {
			if from != Idle {
				i.chime("intercom_end", sharedtone.ToneMute)
			}
		}
	})
	// TEARDOWN: on every transition to Idle from an active state — hangup
	// (button), expired (kill-switch), dropped (turn open failed) — release
	// the shared VoiceSatellite turn. This is keyed on the state transition,
	// NOT on Ended: End() skips Ended for reason "hangup" (it is user-driven),
	// and go-esphome-device clears its turnOpen boolean ONLY on StopTurn or a
	// Run end/error event — a leaked turn bricks the assistant path with
	// ErrTurnInProgress once wake suppression lifts at Idle.
	i.watchTurnRelease(i.releaseTurn)

	// D-P2P-2/7: the direct media leg dies with the session, on the same
	// every-transition-to-Idle hook that releases the turn (hangup from
	// either dot, kill-switch expiry, dropped) — the releaseTurn teardown
	// itself is untouched.
	i.session.Changed = append(i.session.Changed, func(from, to State) {
		if from != Idle && to == Idle {
			i.clearLeg()
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

// watchTurnRelease appends a Changed subscriber that fires teardown on every
// transition into Idle from an active state. Swappable for tests.
func (i *Intercom) watchTurnRelease(fn func()) {
	i.session.Changed = append(i.session.Changed, func(from, to State) {
		if from != Idle && to == Idle {
			fn()
		}
	})
}

// releaseTurn ends the call's audio data path and closes the shared turn
// exactly once per accepted turn (double-close guarded). EndAudio first — the
// probe findings (specs/dropin-probe-findings.md) spell it out as the required
// hang-up binding — then StopTurn, which is the only library call that clears
// turnOpen.
func (i *Intercom) releaseTurn() {
	i.mu.Lock()
	if !i.turnOpen {
		i.mu.Unlock()
		slog.Debug("intercom turn release skipped", "reason", "not open")
		return
	}
	i.turnOpen = false
	i.mu.Unlock()

	if i.turnOps != nil {
		i.turnOps()
		slog.Info("intercom turn released (stub)")
		return
	}
	if err := i.vs.EndAudio(); err != nil {
		slog.Warn("intercom end audio failed", "err", err)
	}
	if err := i.vs.StopTurn(); err != nil {
		slog.Warn("intercom stop turn failed", "err", err)
	}
	slog.Info("intercom turn released")
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
		return
	}
	i.mu.Lock()
	i.turnOpen = true
	i.mu.Unlock()
}

// CallerConnect is the caller-side counterpart of Accept (card A,
// t_d767ce0a): HA invokes dropin_connect on the caller dot once the callee
// accepted. Same turn mechanics, no ring-floor check (the CALLEE's machine
// already enforced it).
func (i *Intercom) CallerConnect() {
	s := i.session
	state, ok := s.CallerJoin()
	if !ok {
		// Expected duplicate/late connect on an InCall dot: idempotent
		// no-op by design (the connect event fires at BOTH endpoints) —
		// not a warn-worthy condition on every call.
		slog.Debug("intercom caller connect refused", "state", state)
		return
	}
	if err := i.vs.StartTurn("", nil); err != nil {
		slog.Error("intercom caller turn open failed", "err", err)
		s.End("dropped")
		// Match Accept(): the session is already gone — do NOT fall through
		// and mark the turn open on a dropped session.
		return
	}
	i.mu.Lock()
	i.turnOpen = true
	i.mu.Unlock()
}

// CallerConnectTo is the D-P2P entry point for the connect event: CallerConnect
// plus the transport/peer facts HA now sends on dropin_connect (D-P2P-2/5/7).
//
// The dot that receives a peer host is the CALLER (D-P2P-5: the caller always
// offers; the callee only ever answers). The callee gets transport but no
// peer, so it simply arms itself and waits for the inbound offer on
// /call/dotoffer — which its session gate authorizes. On the mothballed
// harelays transport nothing here starts; the v1 relay path is unchanged.
func (i *Intercom) CallerConnectTo(session, peer, transport string) {
	i.setTransport(transportOf(transport))
	i.CallerConnect()
	if transportOf(transport) != transportP2P || peer == "" {
		if peer == "" && transportOf(transport) == transportP2P {
			slog.Debug("intercom connect without peer", "session", session, "err", errNoPeer)
		}
		return
	}
	i.mu.Lock()
	live := i.session.Now() != Idle && i.turnOpen
	i.mu.Unlock()
	if !live {
		// CallerJoin refused (duplicate connect on an InCall dot) or the turn
		// could not be opened: there is no session to hang a leg on.
		slog.Debug("intercom leg skipped", "session", session, "state", string(i.session.Now()))
		return
	}
	go i.startCallerLeg(session, peer)
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
	slog.Info("intercom chime", "name", name)
}

// ringChimePeriod is the interval between repeated ring chimes while the
// callee sits in Ringing — long enough for the three-note ToneTimer burst to
// finish with a clear gap before the next.
const ringChimePeriod = 4 * time.Second

// repeatingTimer is the production ring timer: fire once after d, then every
// d thereafter (a Ticker armed on the ring, stopped unconditionally when the
// session leaves Ringing).
func repeatingTimer(d time.Duration, fire func()) (stop func()) {
	var (
		mu    sync.Mutex
		fired bool // stop seen; goroutine drains
		done  = make(chan struct{})
	)
	go func() {
		t := time.NewTimer(d)
		defer t.Stop()
		for {
			select {
			case <-t.C:
				mu.Lock()
				if fired {
					mu.Unlock()
					return
				}
				mu.Unlock()
				fire()
				t.Reset(d)
			case <-done:
				return
			}
		}
	}()
	return func() {
		mu.Lock()
		fired = true
		mu.Unlock()
		close(done)
	}
}

// stopRing kills a live repeat pattern; safe to call from any state, any
// number of times (nil and double-stop are no-ops).
func (i *Intercom) stopRing() {
	i.mu.Lock()
	stop := i.ringStop
	i.ringStop = nil
	i.mu.Unlock()
	if stop != nil {
		stop()
	}
}

// pump streams captured mic frames into the call's audio path. Muted or idle,
// frames are drained and discarded so the listener never backs up (the
// fan-out keeps every frame for slow listeners otherwise).
//
// Routing (D-P2P-7/8): a live direct leg owns call audio and the ESPHome
// voice turn gets NOTHING. The turn is still opened (D-P2P-7: mute/LED/session
// work is transport-agnostic) but carrying call audio on it is exactly what
// let HA's assist pipeline treat the call as an ordinary assistant turn —
// `heard …/replying …` during a call, the v1 relay's death (D-P2P-8). The
// turn is only fed on the mothballed harelays transport, and never while the
// session is p2p.
func (i *Intercom) pump(frames <-chan []int16, vs *esphome.VoiceSatellite) {
	for frame := range frames {
		i.mu.Lock()
		muted := i.muted
		i.mu.Unlock()
		if muted || i.session.Now() == Idle {
			continue
		}
		if leg, useTurn := i.frameDestination(); leg != nil {
			// Anti-ambush (D-DI-3): a ringing dot transmits NOTHING. The leg
			// can already exist while the callee is still ringing — the
			// caller's offer legitimately races its accept — but call audio
			// only flows once the call is actually up (Busy: in-call, muted
			// or joined), never on the ring.
			if !i.session.Busy() {
				continue
			}
			// Direct leg owns call audio (D-P2P-4/6): 16k mono in, one Opus
			// packet out — no resample on either end.
			if err := leg.Send(frame); err != nil {
				slog.Debug("intercom leg send failed", "err", err)
			}
			continue
		} else if !useTurn {
			continue // p2p transport, leg not up yet: never leak into the turn
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

// frameDestination resolves where one captured call frame goes (D-P2P-7/8):
// the live direct leg when one exists, otherwise the ESPHome voice turn — but
// ONLY on the mothballed harelays transport. On p2p the fallback is nothing at
// all: call audio must never reach HA's assist pipeline, which is exactly the
// v1 `heard/replying` hijack that killed the relay (D-P2P-8).
func (i *Intercom) frameDestination() (leg mediaLeg, useTurn bool) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.leg != nil {
		return i.leg, false
	}
	return nil, i.transport == transportHARelays
}

// setLeg publishes the live direct leg (closing any previous one) so the pump
// starts routing to it the moment it exists.
func (i *Intercom) setLeg(l mediaLeg) {
	i.mu.Lock()
	old := i.leg
	i.leg = l
	i.mu.Unlock()
	if old != nil {
		_ = old.Close()
	}
}

// clearLeg closes and forgets the live direct leg (session teardown).
func (i *Intercom) clearLeg() {
	i.mu.Lock()
	leg := i.leg
	i.leg = nil
	i.mu.Unlock()
	if leg != nil {
		_ = leg.Close()
	}
}

// activeLeg is the direct leg currently owning call audio, or nil.
func (i *Intercom) activeLeg() mediaLeg {
	i.mu.Lock()
	defer i.mu.Unlock()
	return i.leg
}

// setTransport records where this session's call audio travels (D-P2P-7).
func (i *Intercom) setTransport(t string) {
	i.mu.Lock()
	i.transport = t
	i.mu.Unlock()
}

// playback resolves the inbound-audio sink: the config.StreamIntercom speaker
// lane in production, an injected sink in tests.
func (i *Intercom) playback() func([]int16) {
	if i.play != nil {
		return i.play
	}
	return i.playIntercom
}

// playLane hands call audio to the intercom mixer lane. It is a var so the
// wiring test can pin the shape handed to the speaker without a device.
var playLane = func(s config.Stream, mono []int16) {
	speaker.Get().PlayMono(s, mono)
}

// playIntercom plays decoded peer audio on the intercom mixer lane
// (config.StreamIntercom — independent volume, D-DI-4).
//
// The producer format here is 16 kHz MONO (the leg's decoded Opus frame, and
// the HA relay's VoiceAssistantAudio payload), which is the INPUT of the
// mic/voice-native path — NOT the lane's own format. The lane is added
// verbatim into the codec's 48 kHz stereo period, so it goes through
// speaker.PlayMono, which resamples and interleaves exactly like PlayVoice
// does for replies (D-P2P-6 as corrected 15 Oct: pushing raw 16 kHz mono put
// 320 samples in 320 of 2048 slots = 3.33 ms of a 20 ms frame, one channel).
func (i *Intercom) playIntercom(samples []int16) {
	playLane(config.StreamIntercom, samples)
}

// PlayPeer plays relayed call audio on the intercom mixer lane. The HA-side
// relay (card 3, mothballed by D-P2P-7) calls this with the peer's
// VoiceAssistantAudio PCM payload; the direct leg feeds the same lane through
// playIntercom.
func (i *Intercom) PlayPeer(pcm []byte) {
	samples := make([]int16, len(pcm)/2)
	for n := range samples {
		samples[n] = int16(binary.LittleEndian.Uint16(pcm[n*2:]))
	}
	i.playback()(samples)
}

// Session exposes the machine for the web/ctl surfaces and tests.
func (i *Intercom) Session() *Session { return i.session }

// Start keeps the wiring a no-op component starter; capture already runs.
func (i *Intercom) Start(context.Context) error { return nil }
