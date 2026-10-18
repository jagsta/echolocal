package intercom

// D-ANN (specs/dot-announce-decisions.md, card 1 of 3): the RECEIVING dot's
// announce playback lane. Capture/serve live in announce.go; the wiring and
// the dropin_announce/dropin_play actions in announce_wiring.go.
//
// Decision map for this file:
//   - D-ANN-9: the receiver chimes FIRST, then fetches and plays. Receivers
//     stay SILENT during the origin's capture window — no ready-tone, nothing
//     until this play nudge arrives (there is deliberately no dot-side hook
//     into someone else's capture; the nudge is HA's, card 2).
//   - D-ANN-10: the assistant pipeline is SUSPENDED for the whole playback —
//     chime, fetch and announce audio — so the receiver's mic cannot ingest
//     the announcement (the 'heard/replying' hazard, the 15 Oct P2P finding).
//   - D-ANN-11: playback LED = purple, a NEW effect distinct from the call
//     lane's green (t_05fc05ad).
//   - D-ANN-13: an IN-CALL receiver SKIPS the announce (logged); a merely
//     RINGING receiver DELAYS it until the call state resolves (bounded).
//   - D-ANN-12: the fetch retries ~10s then skips, logged.

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	sharedtone "github.com/ygelfand/libcountertop/pkg/audio/tone"

	"github.com/ygelfand/echolocal/internal/config"
	"github.com/ygelfand/echolocal/internal/hardware/mic"
)

// fetchPCM is the receiver's fetch over the origin dot's HTTPS listener
// (D-ANN-7/12): plain GET, no pairing ceremony, bounded per attempt. It is a
// var so tests can pin the HTTP shape. It rides peerDotClientTimeout, the
// same validated LAN TLS posture as the P2P signalling hop (p2p_offer.go —
// staged cert, InsecureSkipVerify with its rationale documented there).
var fetchPCM = func(origin string) ([]byte, error) {
	url := "https://" + origin + announcePath
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	return dotGetBody(req, announceFetchTimeout)
}

// dotGetBody issues one bounded GET to a peer dot and returns its raw body,
// mapping statuses the D-ANN-12 retry loop cares about onto sentinel errors.
func dotGetBody(req *http.Request, timeout time.Duration) ([]byte, error) {
	resp, err := peerDotClientTimeout(timeout).Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	switch {
	case resp.StatusCode == http.StatusGone:
		// The capture expired: retrying is hopeless.
		return nil, errGone
	case resp.StatusCode == http.StatusNotFound,
		resp.StatusCode >= http.StatusInternalServerError:
		// Still recording, or the origin hiccuped: retryable (D-ANN-12).
		return nil, errRetryable
	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("announce fetch: status %d", resp.StatusCode)
	}
	out, err := io.ReadAll(io.LimitReader(resp.Body, p2pBodyLimit))
	if err != nil {
		return nil, err
	}
	return out, nil
}

// peerDotClientTimeout is peerDotClient with an explicit timeout (the P2P
// client hard-wires p2pOfferTimeout, which is the wrong budget for ~15s of
// announce PCM).
func peerDotClientTimeout(timeout time.Duration) *http.Client {
	c := *peerDotClient()
	c.Timeout = timeout
	return &c
}

var (
	errRetryable = errors.New("announce fetch retryable")
	errGone      = errors.New("announce fetch: capture gone (410)")
)

// annRetryDelay is D-ANN-12's retry cadence: 1 s apart, 10 attempts ≈ the
// spec's "~10s then skip".
const annRetryDelay = announceFetchInterval

// Play is the dropin_play entry point on a RECEIVER dot: chime first
// (D-ANN-9), then fetch + play with the assistant pipeline suspended
// (D-ANN-10). The lane yields to the call lane first (D-ANN-13). It never
// blocks its caller's read loop: playback runs on its own goroutine.
func (i *Intercom) Play(origin string) {
	a := &i.ann
	if a.now() != annIdle {
		slog.Warn("announce play refused: lane busy", "state", string(a.now()), "origin", origin)
		return
	}
	go a.runPlay(origin)
}

// runPlay is the async half of Play. Order matters and is the spec's: the
// chime comes FIRST (D-ANN-9), and the pipeline suspension covers the chime,
// the fetch and the playback both (D-ANN-10) — the receiver's mic must not
// hear its own chime either.
func (a *announcer) runPlay(origin string) {
	// D-ANN-13: an IN-CALL receiver skips; a RINGING receiver delays until
	// the state resolves (bounded — a ring that never resolves drops it).
	switch lane := a.callLane(); lane {
	case "in-call":
		slog.Warn("announce playback skipped: receiving dot is in-call", "origin", origin)
		return
	case "ringing":
		slog.Info("announce playback delayed: receiving dot is ringing", "origin", origin)
		if !a.awaitCallLaneResolved() {
			slog.Warn("announce playback skipped: the ring never resolved", "origin", origin)
			return
		}
	}

	a.suspend(true)
	defer a.suspend(false)
	a.setState(annPlaying)
	defer a.setState(annIdle)
	if a.setLED != nil {
		a.setLED(true) // purple (D-ANN-11)
		defer a.setLED(false)
	}
	if a.chime != nil {
		a.chime("announce playback", sharedtone.ToneUnmute)
	}

	pcm, err := a.fetchWithRetry(origin)
	if err != nil {
		slog.Warn("announce fetch failed", "origin", origin, "err", err)
		return
	}
	a.playback()(decodePCM16LE(pcm))
	slog.Info("announce played", "origin", origin, "seconds", float64(len(pcm)/2)/float64(mic.Rate))
}

// awaitCallLaneResolved polls the call lane until it leaves RINGING. Swapped
// to a poll rather than a subscription so the seam stays injectable for tests.
func (a *announcer) awaitCallLaneResolved() bool {
	deadline := time.Now().Add(announceRingWait)
	for a.callLane() == "ringing" {
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(announceCapPoll)
	}
	return true
}

// fetchWithRetry is D-ANN-12: retry the fetch ~10s then skip, logged. A 410
// (capture gone) is NOT retried — giving up immediately is cheaper and the
// log line carries the reason.
func (a *announcer) fetchWithRetry(origin string) ([]byte, error) {
	f := a.fetchFn()
	var last error
	for attempt := range announceFetchAttempts {
		raw, err := f(origin)
		if err == nil {
			return raw, nil
		}
		if errors.Is(err, errGone) {
			// Hopeless: the capture expired. D-ANN-12 says skip, not retry.
			return nil, fmt.Errorf("%w after %d attempts: %w", errGone, attempt+1, err)
		}
		last = err
		if attempt < announceFetchAttempts-1 {
			time.Sleep(annRetryDelay)
		}
	}
	return nil, fmt.Errorf("announce fetch gave up after %d attempts: %w", announceFetchAttempts, last)
}

// fetchFn resolves the (origin → raw body) fetcher, injectable for tests.
func (a *announcer) fetchFn() func(string) ([]byte, error) {
	if a.fetch != nil {
		return a.fetch
	}
	return fetchPCM
}

// playback resolves the receiver's announce sink (the intercom mixer lane in
// production; the wiring fills play; tests inject theirs).
func (a *announcer) playback() func([]int16) {
	if a.play != nil {
		return a.play
	}
	return func(samples []int16) { playLane(config.StreamIntercom, samples) }
}

// decodePCM16LE converts the served LE 16-bit PCM to the int16 sink shape.
func decodePCM16LE(raw []byte) []int16 {
	out := make([]int16, len(raw)/2)
	for n := range out {
		out[n] = int16(binary.LittleEndian.Uint16(raw[n*2:]))
	}
	return out
}
