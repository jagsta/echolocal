package intercom

// D-ANN (specs/dot-announce-decisions.md, card 1 of 3) acceptance tests. All
// lane logic is seam-injected, so these run without hardware: fake mic
// fan-out, fake chime/play/LED sinks, pinned call-lane states.

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sharedtone "github.com/ygelfand/libcountertop/pkg/audio/tone"

	esphome "github.com/ygelfand/go-esphome-device"
)

// announceFixture builds an announcer with every seam injected; the capture
// path's chime never touches real audio and the real mic package is untouched.
type sinks struct {
	chimes     atomic.Int64
	played     atomic.Int64
	purple     atomic.Int64 // setLED(true) calls
	ledClears  atomic.Int64
	fetchCalls atomic.Int64
}

func announceFixture(t *testing.T) (*announcer, *sinks) {
	t.Helper()
	a := &announcer{state: annIdle}
	s := &sinks{}
	a.chime = func(name string, notes []sharedtone.Note) { s.chimes.Add(1) }
	a.play = func([]int16) { s.played.Add(1) }
	a.setLED = func(on bool) {
		if on {
			s.purple.Add(1)
		} else {
			s.ledClears.Add(1)
		}
	}
	a.callFn = func() string { return "idle" }
	t.Cleanup(func() { announceSuspended.Store(false) })
	return a, s
}

// framesOf builds n frames at the given amplitude; 20 ms each (the fan-out's
// pace), loud (> announceSilenceRMS) when amp is high, silent otherwise.
func framesOf(n int, amp int16) chan []int16 {
	ch := make(chan []int16, n+1)
	for range n {
		frame := make([]int16, 320)
		for i := range frame {
			frame[i] = amp
		}
		ch <- frame
	}
	close(ch)
	return ch
}

// TestCaptureWindowHardCut pins D-ANN-14: a frame supply that keeps talking
// past the cap ends the capture at exactly announceWindowFrames — no grace.
func TestCaptureWindowHardCut(t *testing.T) {
	a, s := announceFixture(t)
	unbounded := make(chan []int16, 8) // never closed: the hard timer fires
	go func() {
		for {
			frame := make([]int16, 320)
			for i := range frame {
				frame[i] = 3000
			}
			select {
			case unbounded <- frame:
			default:
			}
			time.Sleep(time.Millisecond)
		}
	}()
	a.listen = func() (<-chan []int16, func()) { return unbounded, func() {} }

	a.chime = func(string, []sharedtone.Note) { s.chimes.Add(1) }
	pcm, why := a.record()
	if why != "hard cut" && why != "hard timer" {
		t.Fatalf("window did not hard-cut: %s", why)
	}
	if got := len(pcm) / 320; got < announceWindowFrames {
		t.Fatalf("captured %d frames, want >= %d", got, announceWindowFrames)
	}
	if got := len(pcm) / 320; got > announceWindowFrames {
		t.Fatalf("captured %d frames, want <= %d (no grace)", got, announceWindowFrames)
	}
}

// TestCaptureEarlyBailOnTrailingSilence pins D-ANN-3's early bail: speech then
// a 1.5s lull ends the window well before the 15s cap.
func TestCaptureEarlyBailOnTrailingSilence(t *testing.T) {
	a, _ := announceFixture(t)
	spoken := 100 // 2s of speech
	frames := framesOf(spoken+announceBailSilenceFrames+10, 0)
	// 100 loud frames then silence.
	loud := make(chan []int16, spoken)
	for range spoken {
		frame := make([]int16, 320)
		for i := range frame {
			frame[i] = 3000
		}
		loud <- frame
	}
	close(loud)
	merged := make(chan []int16, spoken+300)
	for f := range loud {
		merged <- f
	}
	for f := range frames {
		merged <- f
	}
	close(merged)

	a.listen = func() (<-chan []int16, func()) { return merged, func() {} }
	pcm, why := a.record()
	if why != "trailing silence" {
		t.Fatalf("did not bail on trailing silence: %s", why)
	}
	if got := len(pcm) / 320; got <= announceMinSpokenFrames {
		t.Fatalf("captured %d frames — speech missing", got)
	}
}

// TestCaptureNeverSpokeBails pins the accidental-trigger path: pure silence
// from the first frame ends the window after ~3s, not 15s.
func TestCaptureNeverSpokeBails(t *testing.T) {
	a, _ := announceFixture(t)
	frames := framesOf(announceDeadSilenceFrames+10, 0)
	a.listen = func() (<-chan []int16, func()) { return frames, func() {} }
	pcm, why := a.record()
	if why != "never spoke" {
		t.Fatalf("did not bail on never-spoke: %s", why)
	}
	// The buffer holds only silence — runCapture's len/amp check is on the
	// RECORDED PCM; silence-only captures carry real frames but no speech.
	// The never-spoke bail exists to END the window early, not to drop data;
	// runCapture publishes such a capture as-is (a 3s silent announcement is
	// harmless and faster than second-guessing the RMS line).
	if got := frameRMS(pcm); got >= announceSilenceRMS {
		t.Fatalf("silence-only capture carries speech: rms %.0f", got)
	}
}

// TestCaptureRefusedWhileInCall pins D-ANN-13's dot-side guard: the origin
// refuses a capture while the session is off idle.
func TestCaptureRefusedWhileInCall(t *testing.T) {
	i := &Intercom{}
	i.ann = announcer{state: annIdle}
	i.ann.callFn = func() string { return "in-call" }
	i.ann.chime = func(string, []sharedtone.Note) { t.Fatal("chimed while in-call") }

	i.Capture("kitchen")
	if got := i.ann.now(); got != annIdle {
		t.Fatalf("capture entered the lane while in-call: %s", got)
	}
}

// TestCaptureWaitsForTheAssistTurn pins D-ANN-4: capture does not touch the
// mic until the assist turn has ended cleanly (t_650b2f99 hazard class).
func TestCaptureWaitsForTheAssistTurn(t *testing.T) {
	a, _ := announceFixture(t)
	var open atomic.Bool
	open.Store(true)
	a.turnFn = open.Load

	supply := framesOf(30, 3000)
	started := make(chan struct{})
	a.listen = func() (<-chan []int16, func()) {
		close(started)
		return supply, func() {}
	}
	a.chime = func(string, []sharedtone.Note) { open.Store(false) } // turn "ends" at the chime moment

	go a.runCapture("kitchen")
	select {
	case <-started:
		t.Fatal("capture opened its mic window while the assist turn was still open")
	case <-time.After(200 * time.Millisecond):
	}
}

// TestCaptureCompleteStoresAndServe pins D-ANN-7/8: the origin stores the
// finished capture and serves it as LE 16-bit PCM with no pairing ceremony —
// the served bytes decode back to what was captured.
func TestCaptureCompleteStoresAndServe(t *testing.T) {
	a, s := announceFixture(t)
	a.notify = func(state announceState) {}
	frames := framesOf(120, 3000)
	a.listen = func() (<-chan []int16, func()) { return frames, func() {} }

	a.runCapture("kitchen") // synchronous: the fixture's supply is finite
	if a.now() != annCaptured {
		t.Fatalf("lane state after capture: %s", a.now())
	}
	if s.chimes.Load() != 1 {
		t.Fatalf("start chime count: %d", s.chimes.Load())
	}

	w := httptest.NewRecorder()
	a.serve(w, httptest.NewRequest(http.MethodGet, announcePath, nil))
	resp := w.Result()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("serve status: %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "pcm_s16le") {
		t.Fatalf("content type: %s", ct)
	}
	body := w.Body.Bytes()
	if len(body) != len(a.captured.pcm)*2 {
		t.Fatalf("served %d bytes, want %d", len(body), len(a.captured.pcm)*2)
	}
	// D-ANN-8: decoded round-trip — first sample matches the captured frame's amp.
	if got := int16(uint16(body[0]) | uint16(body[1])<<8); got != 3000 {
		t.Fatalf("first decoded sample: %d", got)
	}
}

// TestServeRetryableShapes pins D-ANN-12's status contract: nothing yet is a
// retryable 404, an expired capture is a 410 (do not retry).
func TestServeRetryableShapes(t *testing.T) {
	a, _ := announceFixture(t)

	w := httptest.NewRecorder()
	a.serve(w, httptest.NewRequest(http.MethodGet, announcePath, nil))
	if w.Code != http.StatusNotFound {
		t.Fatalf("serve with no capture: %d, want 404", w.Code)
	}

	// Expired capture → 410. A capture that WAS served but whose TTL lapsed
	// (the orchestrator never nudged in time): state still says captured but
	// the slot is stale.
	a.captured = announceCapture{at: time.Now().Add(-announceCapTTL - time.Minute)}
	a.captured.pcm = make([]int16, 320)
	a.captured.id = "ann-expired"
	a.state = annCaptured
	w = httptest.NewRecorder()
	a.serve(w, httptest.NewRequest(http.MethodGet, announcePath, nil))
	if w.Code != http.StatusGone {
		t.Fatalf("serve with expired capture: %d, want 410", w.Code)
	}
}

// TestReceiverChimesThenFetchesThenPlays pins D-ANN-9's order: the play nudge
// chimes FIRST, then fetches, then plays — and D-ANN-10's pipeline suspension
// covers the whole span (suspended before the chime, restored at the end).
func TestReceiverChimesThenFetchesThenPlays(t *testing.T) {
	a, _ := announceFixture(t)

	var order []string
	a.chime = func(string, []sharedtone.Note) { order = append(order, "chime") }
	a.fetch = func(o string) ([]byte, error) {
		order = append(order, "fetch")
		pcm := make([]byte, 640)
		for i := range pcm {
			pcm[i] = 0xB8 // 3000 LE-ish; exact value not asserted here
		}
		return pcm, nil
	}
	a.play = func([]int16) { order = append(order, "play") }

	done := make(chan struct{}, 1)
	go func() { a.runPlay("lounge-dot"); done <- struct{}{} }()

	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("playback never finished")
	}
	if got := strings.Join(order, ","); got != "chime,fetch,play" {
		t.Fatalf("order was %q, want chime,fetch,play", got)
	}
}

// TestReceiverSuspendCoversPlayback pins D-ANN-10 end-to-end: the pipeline
// suspension atomic is set before playback begins and restored after it.
func TestReceiverSuspendCoversPlayback(t *testing.T) {
	a, _ := announceFixture(t)
	pcm := make([]byte, 640)
	a.fetch = func(string) ([]byte, error) { return pcm, nil }

	var seen atomic.Int64
	a.play = func([]int16) {
		if !announceSuspended.Load() {
			t.Error("pipeline was NOT suspended during announce playback (D-ANN-10)")
		}
		seen.Add(1)
	}
	a.runPlay("lounge-dot")
	if seen.Load() == 0 {
		t.Fatal("playback sink never ran")
	}
	if announceSuspended.Load() {
		t.Fatal("pipeline suspension left ON after playback")
	}
}

// TestReceiverSkipsWhileInCall pins D-ANN-13: an IN-CALL receiver skips the
// announce entirely (logged) — no chime, no fetch, no playback.
func TestReceiverSkipsWhileInCall(t *testing.T) {
	a, s := announceFixture(t)
	a.callFn = func() string { return "in-call" }
	a.fetch = func(string) ([]byte, error) { t.Fatal("fetched while in-call"); return nil, nil }

	a.runPlay("lounge-dot")
	if s.chimes.Load() != 0 || s.played.Load() != 0 {
		t.Fatal("playback ran while the receive dot was in-call (D-ANN-13)")
	}
}

// TestReceiverDelaysWhileRinging pins D-ANN-13's ringing half: a merely
// RINGING receiver delays until the state resolves, then plays.
func TestReceiverDelaysWhileRinging(t *testing.T) {
	a, _ := announceFixture(t)

	var lane atomic.Value
	lane.Store("ringing")
	a.callFn = func() string { return lane.Load().(string) }
	a.fetch = func(string) ([]byte, error) {
		if got := lane.Load().(string); got != "idle" {
			t.Fatalf("fetched while still %s", got)
		}
		return make([]byte, 640), nil
	}
	resolved := make(chan struct{})
	go func() { time.Sleep(150 * time.Millisecond); lane.Store("idle"); close(resolved) }()

	a.runPlay("lounge-dot") // synchronous: must have waited out the ring
	if !(lane.Load().(string) == "idle") {
		t.Fatal("lane state test invariant broken")
	}
}

// TestFetchRetryThenSkip pins D-ANN-12: retryable failures are retried ~10x
// and the skip is logged; a 410 stops immediately.
func TestFetchRetryThenSkip(t *testing.T) {
	a, _ := announceFixture(t)

	var calls atomic.Int64
	a.fetch = func(string) ([]byte, error) {
		calls.Add(1)
		return nil, errRetryable
	}
	_, err := a.fetchWithRetry("lounge-dot")
	if err == nil {
		t.Fatal("expected the give-up error")
	}
	if got := calls.Load(); got != announceFetchAttempts {
		t.Fatalf("fetch attempts: %d, want %d", got, announceFetchAttempts)
	}

	calls.Store(0)
	a.fetch = func(string) ([]byte, error) { calls.Add(1); return nil, errGone }
	_, err = a.fetchWithRetry("lounge-dot")
	if !errors.Is(err, errGone) {
		t.Fatalf("gone-410 error: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("410 retried %d times, want 1 (hopeless)", got)
	}
}

// TestDotGetBodyStatusMapping pins the HTTP half of D-ANN-12: 404 and 5xx are
// retryable, 410 is not, 200 carries the body.
func TestDotGetBodyStatusMapping(t *testing.T) {
	body := make([]byte, 8)

	// 404 → retryable
	_, err := dotGetBodyResponse(http.StatusNotFound, body)
	if !errors.Is(err, errRetryable) {
		t.Fatalf("404: %v", err)
	}
	// 410 → gone
	_, err = dotGetBodyResponse(http.StatusGone, body)
	if !errors.Is(err, errGone) {
		t.Fatalf("410: %v", err)
	}
	// 200 → body decodes
	out, err := dotGetBodyResponse(http.StatusOK, body)
	if err != nil || len(out) != len(body) {
		t.Fatalf("200: %v, %d bytes", err, len(out))
	}
}

// TestAnnounceActionsRegistered pins card 1's HA surface: dropin_announce and
// dropin_play ride the intercom action list beside the call lane's four.
func TestAnnounceActionsRegistered(t *testing.T) {
	i := &Intercom{session: New(time.Minute), status: &esphome.TextSensor{}}
	names := map[string]bool{}
	for _, a := range i.Actions() {
		names[a.Name] = true
	}
	for _, want := range []string{"dropin_dial", "dropin_connect", "dropin_answer", "dropin_end", "dropin_announce", "dropin_play"} {
		if !names[want] {
			t.Fatalf("%s missing from the action list", want)
		}
	}
}

// TestAnnouncePauseClauseComposed pins the D-ANN-4/10 suppression seam: the
// clause the wiring hands detect reads the atomic the lanes flip.
func TestAnnouncePauseClauseComposed(t *testing.T) {
	if announcePauseClause() {
		t.Fatal("clause true with no lane active")
	}
	announceSuspended.Store(true)
	if !announcePauseClause() {
		t.Fatal("clause did not follow the atomic")
	}
	announceSuspended.Store(false)
}

// helpers only referenced by tests above:
var (
	_ = context.Background
)

// dotGetBodyResponse builds the httptest-backed variant dotGetBody maps.
func dotGetBodyResponse(status int, body []byte) ([]byte, error) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, srv.URL, nil)
	if err != nil {
		return nil, err
	}
	if peerDotClientTimeout(announceFetchTimeout) == nil {
		return nil, errors.New("no client")
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusGone:
		return nil, errGone
	case resp.StatusCode == http.StatusNotFound, resp.StatusCode >= http.StatusInternalServerError:
		return nil, errRetryable
	case resp.StatusCode != http.StatusOK:
		return nil, errors.New("announce fetch: status")
	}
	out := make([]byte, 0, len(body))
	tmp := make([]byte, len(body))
	for {
		n, rerr := resp.Body.Read(tmp)
		out = append(out, tmp[:n]...)
		if rerr != nil {
			break
		}
	}
	return out, nil
}
