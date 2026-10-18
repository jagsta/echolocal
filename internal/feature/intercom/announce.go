package intercom

// D-ANN (specs/dot-announce-decisions.md, grilled 18 Oct 2026, card 1 of 3):
// dot-side announce machinery — capture and serve. Playback lives in
// announce_play.go, the wiring/actions in announce_wiring.go. Signalling and
// orchestration stay HA-owned (same split as D-P2P-2): the dots expose two
// actions, HA's echolocal_dropin.announce (card 2) drives them.
//
// Decision map for this file:
//   - D-ANN-2: the invoking dot is EXCLUDED from replay. Target resolution is
//     HA's (card 2, drop-in directory); here a capture is one blob any
//     registered dot may pull.
//   - D-ANN-3/4: capture is the PRIMARY mode. Wake-word sentence → HA's
//     intent automation invokes dropin_announce → this dot waits for the
//     assist turn to END cleanly, then chimes and opens a FIXED 15s window
//     with early bail on silence. The t_650b2f99 hazard class (never play or
//     capture from inside an open assist turn) is respected structurally:
//     capture runs on its own goroutine and WAITS for the turn to drain
//     before touching the mic.
//   - D-ANN-6: chime at capture START only; no end-of-capture signal.
//   - D-ANN-7/8: the origin dot stores the capture and serves it on its
//     existing HTTPS web surface. Plain dot-to-dot GET, NO pairing ceremony —
//     the phone gate does NOT apply to announce fetches.
//   - D-ANN-12: a receiver retries ~10s then gives up; a capture not yet
//     finished answers 404, which the retry loop treats as retryable.
//   - D-ANN-13: capture is refused while IN-CALL — the announce lane yields
//     to call audio, dot-side (HA mirrors the guard, card 2).
//   - D-ANN-14: the window hard-cuts at 15s, mid-sentence, no grace.
//
// Config: the spec fixes every knob (15s window, purple, ~10s retry), so NO
// new Intercom config field is introduced — deliberately, because any new
// field must land WITH its PairingTable ctl row in the same commit
// (registration-gap gotcha) and none is needed.

import (
	"encoding/binary"
	"fmt"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	sharedtone "github.com/ygelfand/libcountertop/pkg/audio/tone"

	"github.com/ygelfand/echolocal/internal/hardware/mic"
)

const (
	// announcePath is the origin-dot fetch route (D-ANN-7/8): a plain
	// dot-to-dot GET over the TLS listener, no pairing gate.
	announcePath = "/announce/capture"

	// D-ANN-12: a receiver retries ~10s then gives up (logged). The retry
	// itself lives in card 2's orchestration; the dot-side half is the
	// per-attempt HTTP budget and the retryable/not-retryable STATUS codes.
	announceFetchAttempts = 10
	announceFetchInterval = time.Second
	announceFetchTimeout  = 4 * time.Second

	// D-ANN-3/14: the capture window. Counted in FAN-OUT FRAMES (the mic
	// listener paces exactly one 20 ms frame — mic.FrameSamples — per tick),
	// so the loop needs no clock and stays unit-testable; a hard timer armed
	// slightly past the frame cap is the backstop against a dead fan-out.
	announceWindowFrames = 750 // 750 × 20 ms = 15 s

	// Early-bail thresholds (D-ANN-3). announceSilenceRMS: a leveled frame
	// whose RMS is under this counts as silence. The leveler floors an idle
	// mic far below it and close speech runs well above; the line errs LOW —
	// an early bail LOSES speech, so it must miss silence more eagerly than
	// it misses speech.
	announceSilenceRMS        = 200
	announceBailSilenceFrames = 75  // 1.5 s of trailing silence after speech
	announceMinSpokenFrames   = 50  // under 1 s of speech never early-bails
	announceDeadSilenceFrames = 150 // 3 s of nothing at all bails too

	// announceTurnWait bounds the D-ANN-4 wait for the assist turn to end
	// cleanly before capture opens; announceTurnPoll is the check pace.
	announceTurnWait = 10 * time.Second
	announceTurnPoll = 100 * time.Millisecond

	// announceCapTTL is how long a served capture stays on the origin (one
	// slot, overwritten by the next capture; past this TTL a retry gets 410).
	announceCapTTL = 2 * time.Minute

	// D-ANN-13 (receiver half): a RINGING receiver delays playback until the
	// call state resolves, bounded by announceRingWait (~30s), polled at
	// announceCapPoll.
	announceRingWait = 30 * time.Second
	announceCapPoll  = 100 * time.Millisecond
)

// micFramePeriod is one fan-out tick (mic.Rate/50 = 320 samples = 20 ms).
const micFramePeriod = 20 * time.Millisecond

type announceState string

const (
	annIdle      announceState = "idle"
	annCapturing announceState = "capturing"
	annCaptured  announceState = "captured"
	annPlaying   announceState = "playing"
)

// announceCapture is one finished announcement held on the origin dot.
type announceCapture struct {
	id   string // "ann-<unixnano>" log/join key, mirrored in the X-Announce-Id header
	room string // announced target ("" = broadcast); informational
	pcm  []int16
	at   time.Time
}

// captureDone is a ONE-deep hand-off: the origin's capture goroutine parks the
// finished capture where card 2's HA orchestrator can observe completion (the
// session sensor flips to "captured" the same moment). A single slot suits the
// single-announce semantics — a second capture displaces a still-unobserved
// one, exactly like a second person stepping up to the dot.
var captureDone = make(chan announceCapture, 1)

// announceSuspended is the pipeline-pause clause wired into both suppression
// seams (announce_wiring.go): while true, wake detection is paused AND the
// voice guard blocks turns — the assistant pipeline is suspended. Atomic
// because the detect engine reads it from its own capture goroutine.
var announceSuspended atomic.Bool

// announcer owns the dot-side announce lane, one per dot. It shares the
// Intercom singleton (session machine, LED claim, chime seam) but keeps its
// own mutex: capture and playback run on goroutines of their own and every
// entry point refuses/retries rather than blocking — neither the API action
// handler nor the HTTP handler may wait on the lane (t_650b2f99 class).
type announcer struct {
	mu       sync.Mutex
	state    announceState
	captured announceCapture // valid while state == annCaptured (ONE slot)

	// seams, filled by wire (announce_wiring.go); tests inject their own.
	chime  func(name string, notes []sharedtone.Note)
	listen func() (<-chan []int16, func())
	fetch  func(o string) ([]byte, error) // origin hostname → PCM bytes
	play   func(pcm []int16)
	turnFn func() bool   // assist turn open? nil = never open (tests, no voice loop)
	callFn func() string // "idle"|"ringing"|"in-call"; nil = idle (tests)
	setLED func(on bool) // purple on/off around playback (D-ANN-11)
	notify func(state announceState)
}

// suspend pauses/restores the assistant pipeline around lane audio (D-ANN-4/10).
func (a *announcer) suspend(on bool) { announceSuspended.Store(on) }

func (a *announcer) setState(s announceState) {
	a.mu.Lock()
	a.state = s
	a.mu.Unlock()
	if a.notify != nil {
		a.notify(s)
	}
}

func (a *announcer) now() announceState {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.state
}

func (a *announcer) turnOpen() bool { return a.turnFn != nil && a.turnFn() }

// callLane reports this dot's session lane as announce sees it (D-ANN-13).
func (a *announcer) callLane() string {
	if a.callFn != nil {
		return a.callFn()
	}
	return "idle"
}

//
// ─── CAPTURE (D-ANN-3/4/6/13/14) ─────────────────────────────────────────────

// Capture handles the dropin_announce action on the ORIGIN dot: refuse on any
// contention, else run the window on its own goroutine (the API read loop
// must never wait — third_party conn.go's handler contract). HA learns
// completion from the session sensor; the dot stores + serves the capture.
func (i *Intercom) Capture(room string) {
	a := &i.ann
	if lane := a.callLane(); lane == "in-call" {
		// D-ANN-13: refused while IN-CALL. Enforced dot-side regardless of
		// HA's orchestration view — never collide with call audio.
		slog.Warn("announce capture refused: in-call", "room", room)
		return
	}
	if a.now() != annIdle {
		slog.Warn("announce capture refused: lane busy", "room", room, "state", string(a.now()))
		return
	}
	go a.runCapture(room)
}

// runCapture waits for the assist turn to end cleanly (D-ANN-4), chimes at
// start (D-ANN-6), records the 15s window, then stores + publishes. A capture
// whose window overlapped a call start is DISCARDED at the store step: call
// audio would be in it (D-ANN-13 mid-capture yield).
func (a *announcer) runCapture(room string) {
	// D-ANN-4: mic ownership moves away from the assistant pipeline BEFORE
	// capture starts. The intent automation fires while the pipeline is still
	// finishing its spoken reply; wait for it to drain rather than racing it.
	deadline := time.Now().Add(announceTurnWait)
	for a.turnOpen() {
		if time.Now().After(deadline) {
			slog.Warn("announce capture aborted: the assist turn never ended", "room", room)
			return
		}
		time.Sleep(announceTurnPoll)
	}

	a.mu.Lock()
	if a.state != annIdle { // a second announce raced the turn-wait: loses
		a.mu.Unlock()
		slog.Warn("announce capture refused: lane busy on arrival", "room", room)
		return
	}
	a.state = annCapturing
	a.mu.Unlock()
	a.suspend(true)
	defer a.suspend(false)

	// D-ANN-6: chime at capture START only; nothing marks the end.
	if a.chime != nil {
		a.chime("announce capture", sharedtone.ToneTimer)
	}

	pcm, why := a.record()
	if lane := a.callLane(); lane != "idle" && lane != "ringing" {
		slog.Warn("announce capture discarded: a call started mid-capture")
		a.setState(annIdle)
		return
	}
	if len(pcm) == 0 {
		slog.Warn("announce capture empty", "why", why)
		a.setState(annIdle)
		return
	}

	cap := announceCapture{
		id:   fmt.Sprintf("ann-%d", time.Now().UnixNano()),
		room: room,
		pcm:  pcm,
		at:   time.Now(),
	}
	a.mu.Lock()
	a.captured = cap
	a.state = annCaptured
	a.mu.Unlock()
	slog.Info("announce captured", "id", cap.id, "room", room, "why", why,
		"seconds", float64(len(pcm))/float64(mic.Rate))

	select {
	case captureDone <- cap:
	default: // a prior unobserved capture is displaced; one slot, by design
	}
}

// record drains the mic fan-out into a 16k mono buffer until the window's
// edge: hard cap at announceWindowFrames (D-ANN-14 — mid-sentence, no grace)
// with early bail on trailing silence (D-ANN-3). Bail reasons are logged,
// never signalled: D-ANN-6 forbids an end-of-capture cue on the origin dot.
func (a *announcer) record() (pcm []int16, why string) {
	listen := a.listen
	if listen == nil {
		listen = func() (<-chan []int16, func()) { return mic.Get().Listen("announce") }
	}
	frames, unlisten := listen()
	defer unlisten()

	// D-ANN-14 backstop: if the fan-out dies mid-window the frame loop would
	// hang forever, so a hard timer just past 15 s cuts it.
	hard := time.NewTimer((announceWindowFrames + 300) * micFramePeriod)
	defer hard.Stop()

	var (
		buf      []int16
		spoken   int
		heard    int
		total    int
	)
	for {
		select {
		case frame, ok := <-frames:
			if !ok {
				return buf, "fan-out closed"
			}
			buf = append(buf, frame...)
			spoken++
			if frameRMS(frame) < announceSilenceRMS {
				total++
				switch {
				// An accidental trigger (pure noise floor, nothing ever
				// heard) must not hold the mic for 15 s.
				case heard == 0 && total >= announceDeadSilenceFrames:
					return buf, "never spoke"
				// After real speech, a trailing lull ends the window.
				case heard >= announceMinSpokenFrames && spoken-heard >= announceBailSilenceFrames:
					return buf, "trailing silence"
				}
			} else {
				heard++
			}
			if spoken >= announceWindowFrames {
				return buf, "hard cut" // D-ANN-14: mid-sentence, no grace
			}
		case <-hard.C:
			return buf, "hard timer"
		}
	}
}

// frameRMS is the frame's RMS on a linear 0..32767 scale. int64 math
// throughout: 320 samples × 32768² ≈ 3.4e11 overflows int32 on armv7.
func frameRMS(frame []int16) float64 {
	if len(frame) == 0 {
		return 0
	}
	var total int64
	for _, s := range frame {
		total += int64(s) * int64(s)
	}
	return math.Sqrt(float64(total) / float64(len(frame)))
}

//
// ─── SERVE (D-ANN-7/8/12) ────────────────────────────────────────────────────

// serve implements the origin half of the announce fetch: a plain GET of the
// finished capture as LE 16-bit PCM over the dot's HTTPS listener. D-ANN-8:
// NO pairing gate — the phone ceremony is not applied to announce fetches.
//
// D-ANN-12 shape: a capture still recording (or none yet) answers 404, which
// card 2's retry loop treats as retryable; a capture past its TTL is a 410 —
// retrying that is hopeless, so the loop should give up immediately.
func (a *announcer) serve(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	cap, state := a.captured, a.state
	a.mu.Unlock()

	if state == annIdle && cap.at.IsZero() {
		http.Error(w, "no announcement", http.StatusNotFound)
		return
	}
	if state != annCaptured || len(cap.pcm) == 0 {
		http.Error(w, "announcement is being recorded", http.StatusNotFound)
		return
	}
	if time.Since(cap.at) > announceCapTTL {
		http.Error(w, "announcement no longer held", http.StatusGone)
		return
	}

	w.Header().Set("Content-Type", "audio/pcm;codec=pcm_s16le;rate=16000;channels=1")
	w.Header().Set("X-Announce-Id", cap.id)
	w.Header().Set("Content-Length", strconv.Itoa(len(cap.pcm)*2))
	w.WriteHeader(http.StatusOK)

	// LE int16 PCM, one buffer, streamed by the listener's own chunking.
	buf := make([]byte, len(cap.pcm)*2)
	for n, s := range cap.pcm {
		binary.LittleEndian.PutUint16(buf[n*2:], uint16(s))
	}
	if _, err := w.Write(buf); err != nil {
		slog.Debug("announce serve write failed", "err", err)
	}
}
