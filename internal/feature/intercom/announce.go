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
//     assist turn to END cleanly, then opens a FIXED 15s window with early
//     bail on silence. The t_650b2f99 hazard class (never play or capture
//     from inside an open assist turn) is respected structurally: capture
//     runs on its own goroutine and WAITS for the turn to drain before
//     touching the mic.
//   - D-ANN-6: NO capture chime (17a, Jag ruling 19 Oct: it collided with
//     the prompt TTS) and no end-of-capture signal; the spoken prompt and
//     the D-ANN-16 listen-blue are the user's cues.
//   - D-ANN-16: the capture window claims the listen-blue LED.
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
	// Calibration 19 Oct (live lounge, probe-lounge-speech.wav, ch2): speech
	// at room distance levels at −50..−60 dBFS RMS-frames (median frame RMS
	// 79, loud bursts ~1100); 200 (−44 dBFS) counted nearly ALL of it as
	// silence → heard=45 < announceMinSpokenFrames on a real announce →
	// publish abort → receiver 404, no playback. 64 (−54 dBFS) catches ~50%
	// of speech frames; ambient floor (median 16, transient spikes) still
	// stays under it in the median frame. Cost: a noisy room may keep
	// `quiet` from reaching the bails, so capture runs the full 15s window —
	// acceptable; missing the announce is not.
	announceSilenceRMS = 64
	// Live 19 Oct: the AEC-sounding window (chime + "Speak now." tail, ~2s
	// POST-lead-in) is digital SILENCE — the canceller removes the bleed, so
	// those frames are genuinely quiet and every one of them used to count
	// toward BOTH bails: (a) trailing-silence fired TOTAL-based, so the first
	// breath pause after ~1s of speech ended a mid-sentence capture (probe5
	// cut at exactly 250 frames), and (b) dead-silence fired ~1.5s after the
	// TTS finished, before a natural reaction could begin (probe1-4: every
	// human test "never spoke" seconds=4.5).
	announceBailSilenceFrames = 150 // 3 s of CONTIGUOUS trailing silence (Jag ruling 19 Oct: 1.5s cut mid-sentence)
	announceMinSpokenFrames   = 50  // under 1 s of speech never early-bails
	announceDeadSilenceFrames = 300 // 6 s of contiguous dead air bails too

	// announceTurnWait bounds the D-ANN-4 wait for the assist turn to end
	// cleanly before capture opens; announceTurnPoll is the check pace.
	announceTurnWait = 10 * time.Second
	announceTurnPoll = 100 * time.Millisecond

	// announceRecordLeadInFrames excludes the chime/speak-now tail from the
	// speech counters (75 frames = 1.5 s): the UX reorder chimes AT invoke,
	// and its tail couples into the mic right where record() begins.
	announceRecordLeadInFrames = 75

	// D-ANN-18 settle cue: the drain tail keeps the feed loud/cancelled for
	// ~2s past the prompt; the first genuine quiet lull inside the early
	// span lights the capture LED (the "go" moment — dark until then).
	announceSettleQuietFrames = 10  // 200ms contiguous quiet
	announceSettleSpanFrames  = 300 // cue can only fire in the first 6s

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

	captureLED func(on bool) // listen-blue on/off around the capture window (D-ANN-16)
	notify     func(state announceState)
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
	// D-ANN UX: the spoken prompt is HA's reply on the invoking dot itself
	// ("What's the announcement?", 17a — an earlier dot-side chime rode the
	// invoke but the prompt TTS masked it on the same speaker; Jag ruled the
	// chime AWAY 19 Oct). Capture claims the lane here and hands it to the
	// runCapture goroutine.
	// NOTE: no deferred reset here — Capture hands the lane to the
	// runCapture goroutine (still annCapturing across the turn-wait); a
	// defer would fire at Capture's return and reset the lane to idle
	// beneath the goroutine (live 18 Oct: busy-on-arrival refusal, live 21:07
	// test). All exit paths own their own setState.
	if lane := a.callLane(); lane == "in-call" {
		// D-ANN-13: refused while IN-CALL. Enforced dot-side regardless of
		// HA's orchestration view — never collide with call audio.
		slog.Warn("announce capture refused: in-call", "room", room)
		return
	}
	a.mu.Lock()
	// D-ANN: one slot, overwritten by the NEXT capture. A finished (annCaptured)
	// slot is retired by a new Capture — 19 Oct live: the lane only otherwise
	// clears at TTL+serve, so an announce within 2 minutes of the last one was
	// refused AND the orchestration re-played the PREVIOUS capture instead.
	// Only in-flight states (annCapturing, annPlaying) refuse.
	if a.state != annIdle && a.state != annCaptured { // a second announce raced: loses
		a.mu.Unlock()
		slog.Warn("announce capture refused: lane busy", "room", room, "state", string(a.state))
		return
	}
	if a.state == annCaptured {
		slog.Info("announce capture superseded a previous slot", "room", room)
	}
	a.state = annCapturing
	a.mu.Unlock()
	go a.runCapture(room)
}

// runCapture waits for the assist turn to end cleanly (D-ANN-4), then records
// the 15s window and stores + publishes. A capture whose window overlapped a
// call start is DISCARDED at the store step: call audio would be in it
// (D-ANN-13 mid-capture yield).
func (a *announcer) runCapture(room string) {
	// D-ANN-4: mic ownership moves away from the assistant pipeline BEFORE
	// capture starts. The intent automation fires while the pipeline is still
	// finishing its spoken reply; wait for it to drain rather than racing it.
	deadline := time.Now().Add(announceTurnWait)
	for a.turnOpen() {
		if time.Now().After(deadline) {
			slog.Warn("announce capture aborted: the assist turn never ended", "room", room)
			a.setState(annIdle)
			return
		}
		time.Sleep(announceTurnPoll)
	}

	a.mu.Lock()
	if a.state != annCapturing { // the turn-wait lost a race with teardown
		a.mu.Unlock()
		slog.Warn("announce capture refused: lane busy on arrival", "room", room)
		return
	}
	a.mu.Unlock()
	a.suspend(true)
	defer a.suspend(false)
	// D-ANN-18 (Jag ruling 19 Oct, raw-capture DECLINED): instead of
	// capturing the raw mic, the origin tells the user when the feed is
	// usable. The dot's speaker drains ~2s past "turn ended" (HA TTS
	// chunking + the dot's media queue), the canceller zero-suppresses the
	// feed the whole time (level-based; see cancel.go sounding()), and any
	// words spoken into that span are destroyed — live: "Hello Jasmine"
	// vanished at a 1s natural pause. So the capture LED claim is DELAYED
	// to the settle moment: the first stretch of ~200ms contiguous quiet
	// inside the early span. Dark = not yet, listen-blue = speak now.
	// D-ANN-18b (19 Oct witness): the drain residue was still being
	// BUFFERED — waiting for the blue then left ~2s of dead space at the
	// head of the recording. buf therefore also STARTS at settle: the
	// pre-settle frames keep driving the counters but never publish.
	// A capture that never settles (continuous speech, no lull) keeps
	// everything — the old behavior. Still fires at most once.
	if a.captureLED != nil {
		defer a.captureLED(false)
	}

	pcm, why, heard := a.record()
	if lane := a.callLane(); lane != "idle" && lane != "ringing" {
		slog.Warn("announce capture discarded: a call started mid-capture")
		a.setState(annIdle)
		return
	}
	if heard < announceMinSpokenFrames {
		// D-ANN-3 (19 Oct live): a capture the user never meaningfully spoke
		// into is an ABORT, not a servable capture — promoting it wedged the
		// lane in annCaptured (HA plays silence, nothing fetches, every later
		// announce refused 'lane busy') until a serve()'s lazy TTL demote.
		// Return to idle; the receiver's retry loop gets a clean 404 and the
		// lane is free for the next announce.
		slog.Warn("announce capture aborted", "why", why, "heard", heard)
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
func (a *announcer) record() (pcm []int16, why string, heard int) {
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

	// D-ANN UX: capture starts only after record()'s own waiting; record()
	// skips its own lead-in before speech counting begins. The lead-in
	// (announceRecordLeadInFrames) still guards the first ~1.5s of frame
	// counting: the PROMPT tail ("What's the announcement?", HA's TTS reply
	// on the invoke) couples into the mic and would be counted as speech
	// (live 18 Oct: 22 bleed frames deadlocked every silence bail — heard>0
	// kills dead-silence, heard<50 blocks trailing-bail — 13.6s of true
	// silence recorded until hard cut).
	var (
		buf      []int16
		spoken   int
		quiet    int                   // CONTIGUOUS silence: reset on any post-lead-in speech frame
		settleQ  int                   // D-ANN-18c: contiguous quiet COUNTING the lead-in
		settled  = a.captureLED == nil // D-ANN-18: cue fires once
		bufStart int                   // D-ANN-18b: publishable frames begin here
	)
	// tail returns the published capture (minus any drained pre-settle span).
	tail := func() []int16 { return buf[bufStart*320:] }
	for {
		select {
		case frame, ok := <-frames:
			if !ok {
				return tail(), "fan-out closed", heard
			}
			spoken++
			leadIn := spoken <= announceRecordLeadInFrames
			buf = append(buf, frame...)
			if frameRMS(frame) < announceSilenceRMS {
				settleQ++
				if !leadIn {
					quiet++
					switch {
					// An accidental trigger (pure noise floor, nothing ever
					// heard) must not hold the mic for 15 s.
					case heard == 0 && quiet >= announceDeadSilenceFrames:
						return tail(), "never spoke", heard
					// After real speech, a trailing lull ends the window.
					case heard >= announceMinSpokenFrames && quiet >= announceBailSilenceFrames:
						return tail(), "trailing silence", heard
					}
				}
			} else if !leadIn {
				heard++
				quiet = 0
			}
			if frameRMS(frame) < announceSilenceRMS {
			} else {
				settleQ = 0
			}
			// D-ANN-18: the "go" cue — first real lull (>200ms) lights the
			// listen-purple once; 18b: the publishable capture STARTS there
			// (drain residue stays out of the recording); 18c: the settle
			// counts the lead-in too — with no prompt TTS the feed is quiet
			// from frame 1 and the purple lights ~200ms after window-open
			// instead of ~1.7s.
			if !settled && spoken <= announceSettleSpanFrames && settleQ >= announceSettleQuietFrames {
				settled = true
				bufStart = len(buf) / 320
				a.captureLED(true)
				// prototype 2 (19 Oct, Jag ask): a non-TTS tick rides the go
				// cue — 80ms ToneVolume, inaudible bleed (~2 frames), fired
				// AT settle so it never pre-dates the publishable span.
				a.chime("announce go", sharedtone.ToneVolume)
			}
			if spoken >= announceWindowFrames {
				return tail(), "hard cut", heard
			}
		case <-hard.C:
			return tail(), "hard timer", heard
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
		// D-ANN-18d: the lane-state header lets HA tell "still recording"
		// from "the capture aborted / nothing in flight" — the second one
		// must NOT trigger the D-ANN-12 nudge-anyway (19 Oct live: the
		// receiver lit up for a slot that would never exist).
		w.Header().Set("X-Announce-State", string(annIdle))
		http.Error(w, "no announcement", http.StatusNotFound)
		return
	}
	if state != annCaptured || len(cap.pcm) == 0 {
		w.Header().Set("X-Announce-State", string(state))
		http.Error(w, "announcement is being recorded", http.StatusNotFound)
		return
	}
	if time.Since(cap.at) > announceCapTTL {
		// Lazy demote: an expired slot returns the lane to idle instead of
		// wedging Capture/Play gates on a capture nobody will ever serve
		// (live 18 Oct: kitchen stuck in 'captured' refused every nudge).
		a.mu.Lock()
		if a.state == annCaptured && time.Since(a.captured.at) > announceCapTTL {
			a.captured = announceCapture{}
			a.state = annIdle
			st := annIdle
			a.mu.Unlock()
			if a.notify != nil {
				a.notify(st)
			}
		} else {
			a.mu.Unlock()
		}
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
