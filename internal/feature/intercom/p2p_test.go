package intercom

// D-P2P unit tests (specs/dot-p2p-decisions.md, a55be20). No hardware, no HA:
// the codec/ICE shape, the callee offer gate, the mic routing rule, and one
// real in-process pion negotiation prove the leg end to end.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pion/webrtc/v4"
	esphome "github.com/ygelfand/go-esphome-device"

	"github.com/ygelfand/echolocal/internal/config"
	"github.com/ygelfand/echolocal/internal/feature/voice"
)

// fakeLeg is a mediaLeg stand-in: it records what the pump handed it.
type fakeLeg struct {
	mu     sync.Mutex
	frames [][]int16
	closed bool
}

func (f *fakeLeg) Send(pcm []int16) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.frames = append(f.frames, append([]int16(nil), pcm...))
	return nil
}

func (f *fakeLeg) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *fakeLeg) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.frames)
}

func (f *fakeLeg) wasClosed() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

func testIntercom(play func([]int16)) *Intercom {
	return &Intercom{session: New(time.Minute), status: &esphome.TextSensor{}, play: play}
}

// TestP2PCodecIs16kMono pins D-P2P-6: the dot↔dot track is Opus 16kHz MONO —
// the mic's native rate, so neither dot resamples on the capture/encode side —
// never the phone leg's 48k stereo. The inbound half is converted to the
// codec's 48k stereo layout at the speaker seam, not here (D-P2P-6 correction
// of 15 Oct, review F1 on this card; pinned by TestPlayIntercomPushConverts).
func TestP2PCodecIs16kMono(t *testing.T) {
	c := p2pCodec()
	if c.MimeType != webrtc.MimeTypeOpus {
		t.Fatalf("mime = %q, want %q", c.MimeType, webrtc.MimeTypeOpus)
	}
	if c.ClockRate != 16000 {
		t.Fatalf("clock rate = %d, want 16000 (D-P2P-6)", c.ClockRate)
	}
	if c.Channels != 1 {
		t.Fatalf("channels = %d, want 1 (MONO, D-P2P-6)", c.Channels)
	}
	// The encoder/decoder view must not drift from the capability line.
	if p2pClockRate != c.ClockRate {
		t.Fatalf("p2pClockRate = %d but the track advertises %d", p2pClockRate, c.ClockRate)
	}
	if p2pChannels != c.Channels {
		t.Fatalf("p2pChannels = %d but the track advertises %d", p2pChannels, c.Channels)
	}
}

// TestP2PConfigIsHostCandidateOnly pins D-P2P-5: no STUN/TURN servers, so pion
// can only emit host candidates (OnlyCandidateTypeHost).
func TestP2PConfigIsHostCandidateOnly(t *testing.T) {
	cfg := p2pConfig()
	if len(cfg.ICEServers) != 0 {
		t.Fatalf("ICEServers = %d entries, want 0 (host-only, D-P2P-5)", len(cfg.ICEServers))
	}
	for _, s := range cfg.ICEServers {
		if len(s.URLs) > 0 {
			t.Fatalf("unexpected ICE server url %q", s.URLs)
		}
	}
	if cfg.BundlePolicy != 0 {
		// Only documents that nothing else is being configured here.
		t.Fatalf("unexpected bundle policy %v", cfg.BundlePolicy)
	}
}

// TestDotLegOfferSDPIsHostOnly16k proves the same two decisions in the wire
// format: the gathered offer advertises opus/16000 and carries host candidates
// only, with no srflx/relay candidate lines (D-P2P-5/6).
func TestDotLegOfferSDPIsHostOnly16k(t *testing.T) {
	leg, err := newDotLeg(func([]int16) {})
	if err != nil {
		t.Fatalf("newDotLeg: %v", err)
	}
	defer func() { _ = leg.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	offer, err := leg.Offer(ctx)
	if err != nil {
		t.Fatalf("offer: %v", err)
	}
	if !strings.Contains(offer, "opus/16000") {
		t.Fatalf("offer does not advertise opus/16000:\n%s", offer)
	}
	if strings.Contains(offer, "opus/48000") {
		t.Fatalf("offer advertises the phone leg's 48k capability:\n%s", offer)
	}
	if !strings.Contains(offer, "typ host") {
		t.Fatalf("offer has no host candidate:\n%s", offer)
	}
	for _, bad := range []string{"typ srflx", "typ relay", "typ prflx"} {
		if strings.Contains(offer, bad) {
			t.Fatalf("offer contains a non-host candidate (%q) — D-P2P-5 violated:\n%s", bad, offer)
		}
	}
}

// TestOfferAuthorizedGate covers the D-P2P-4/5 gate states: ringing answers,
// an accepted/connected call answers, idle or ended refuse.
func TestOfferAuthorizedGate(t *testing.T) {
	s := New(time.Minute)
	if s.OfferAuthorized() {
		t.Fatal("idle session authorized an offer — the gate must fail closed")
	}
	if !s.Ring() {
		t.Fatal("ring refused")
	}
	if !s.OfferAuthorized() {
		t.Fatal("ringing session must answer: HA's dropin_dial is the authorization")
	}
	// The offer may race the callee's own accept; both states must answer.
	s.clock = func() time.Time { return time.Now().Add(RingFloor + time.Second) }
	if _, ok := s.Accept(); !ok {
		t.Fatal("accept refused")
	}
	if !s.OfferAuthorized() {
		t.Fatal("in-call session (accept/connect) must answer")
	}
	if !s.End("hangup") {
		t.Fatal("end refused")
	}
	if s.OfferAuthorized() {
		t.Fatal("ended session authorized an offer — the gate must close on teardown")
	}
}

// TestDotOfferHandlerRefusesUnauthorized pins the HTTP half of the gate: an
// idle dot answers 403 and never builds a media leg (mirror of the phone
// path's pairing gate, D-DI-9b).
func TestDotOfferHandlerRefusesUnauthorized(t *testing.T) {
	i := testIntercom(func([]int16) {})
	body, _ := dotOfferMsg{Call: "di-1", SDP: "v=0\r\n"}.encode()
	rec := httptest.NewRecorder()
	i.DotOfferHandler().ServeHTTP(rec, httptest.NewRequest(http.MethodPost, dotOfferPath, bytes.NewReader(body)))
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (gate must refuse a non-ringing/unauthorized dot)", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "refused") {
		t.Fatalf("refusal body = %q", rec.Body.String())
	}
	if leg := i.activeLeg(); leg != nil {
		t.Fatal("refused offer still created a media leg")
	}
}

func TestDotOfferHandlerRejectsMalformed(t *testing.T) {
	i := testIntercom(func([]int16) {})
	for _, body := range []string{"", "{", `{"call":"di-1"}`, `{"sdp":"v=0"}`} {
		rec := httptest.NewRecorder()
		i.DotOfferHandler().ServeHTTP(
			rec, httptest.NewRequest(http.MethodPost, dotOfferPath, strings.NewReader(body)),
		)
		if rec.Code != http.StatusBadRequest {
			t.Fatalf("body %q: status = %d, want 400", body, rec.Code)
		}
	}
}

// TestDotOfferLoopbackCarriesAudio is the end-to-end proof of D-P2P-4/5/6 in
// process: a real caller leg (offer) negotiates against the callee's real
// answer handler, the two PeerConnections connect over host candidates, and
// 20ms 16kHz-mono frames pushed into one side come out decoded on the other.
func TestDotOfferLoopbackCarriesAudio(t *testing.T) {
	calleeAudio := make(chan []int16, 64)
	callerAudio := make(chan []int16, 64)
	callee := testIntercom(func(pcm []int16) {
		select {
		case calleeAudio <- pcm:
		default:
		}
	})
	caller := testIntercom(func(pcm []int16) {
		select {
		case callerAudio <- pcm:
		default:
		}
	})

	// Callee state: HA's dropin_dial has rung it (authorization).
	if !callee.session.Ring() {
		t.Fatal("callee ring refused")
	}
	// Caller state: HA's connect already ran on it.
	if _, ok := caller.session.CallerJoin(); !ok {
		t.Fatal("caller join refused")
	}

	// In-process signalling: the caller's POST lands on the callee's real
	// handler and the answer comes back in the response body (D-P2P-4/5) —
	// the only thing replaced is the TLS hop.
	caller.post = func(_ context.Context, _ string, m dotOfferMsg) (string, error) {
		body, err := m.encode()
		if err != nil {
			return "", err
		}
		rec := httptest.NewRecorder()
		callee.DotOfferHandler().ServeHTTP(
			rec, httptest.NewRequest(http.MethodPost, dotOfferPath, bytes.NewReader(body)),
		)
		if rec.Code != http.StatusOK {
			return "", fmt.Errorf("callee answered %d: %s", rec.Code, rec.Body.String())
		}
		var answer dotOfferMsg
		if err := json.Unmarshal(rec.Body.Bytes(), &answer); err != nil {
			return "", err
		}
		return answer.SDP, nil
	}

	caller.startCallerLeg("di-loopback", "self")
	callerLeg, ok := caller.activeLeg().(*dotLeg)
	if !ok || callerLeg == nil {
		t.Fatal("caller leg was not started")
	}
	calleeLeg, ok := callee.activeLeg().(*dotLeg)
	if !ok || calleeLeg == nil {
		t.Fatal("callee leg was not created by the answer handler")
	}
	defer func() {
		caller.clearLeg()
		callee.clearLeg()
	}()

	if !waitState(callerLeg, webrtc.PeerConnectionStateConnected, 20*time.Second) {
		t.Fatalf("caller leg never connected (state %v)", callerLeg.State())
	}
	if !waitState(calleeLeg, webrtc.PeerConnectionStateConnected, 20*time.Second) {
		t.Fatalf("callee leg never connected (state %v)", calleeLeg.State())
	}

	// Caller mic → callee speaker lane, and back (both dots send AND receive).
	frame := make([]int16, p2pSamples)
	for n := range frame {
		frame[n] = int16(2000 * (n % 7) / 7)
	}
	deadline := time.Now().Add(15 * time.Second)
	sent := 0
	for sent < 50 && time.Now().Before(deadline) {
		_ = callerLeg.Send(frame)
		_ = calleeLeg.Send(frame)
		sent++
		if len(calleeAudio) > 0 && len(callerAudio) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	select {
	case got := <-calleeAudio:
		if len(got) == 0 {
			t.Fatal("callee played an empty frame")
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("no audio decoded on the callee after %d frames (sent=%d recv=%d)",
			sent, calleeLeg.Sent(), calleeLeg.Recv())
	}
	select {
	case got := <-callerAudio:
		if len(got) == 0 {
			t.Fatal("caller played an empty frame")
		}
	case <-time.After(5 * time.Second):
		t.Fatalf("no audio decoded on the caller (sent=%d recv=%d)", callerLeg.Sent(), callerLeg.Recv())
	}
}

func waitState(l *dotLeg, want webrtc.PeerConnectionState, within time.Duration) bool {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if l.State() == want {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}

// TestFrameDestination pins D-P2P-7/8: the direct leg owns call audio whenever
// it exists, the ESPHome voice turn is reachable ONLY on the mothballed
// harelays transport, and a p2p dot whose leg is not up yet drops the frame
// rather than leaking call audio into HA's assist pipeline.
func TestFrameDestination(t *testing.T) {
	cases := []struct {
		name     string
		leg      bool
		set      string
		wantLeg  bool
		wantTurn bool
	}{
		{"p2p with leg", true, transportP2P, true, false},
		{"p2p without leg", false, transportP2P, false, false},
		{"p2p default without leg", false, "", false, false},
		{"harelays without leg", false, transportHARelays, false, true},
		{"unset transport with leg", true, "", true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			i := testIntercom(func([]int16) {})
			if tc.set != "" {
				i.setTransport(tc.set)
			} else {
				i.setTransport(transportOf(""))
			}
			if tc.leg {
				i.setLeg(&fakeLeg{})
			}
			leg, useTurn := i.frameDestination()
			if (leg != nil) != tc.wantLeg {
				t.Fatalf("leg = %v, want %v", leg != nil, tc.wantLeg)
			}
			if useTurn != tc.wantTurn {
				t.Fatalf("useTurn = %v, want %v", useTurn, tc.wantTurn)
			}
		})
	}
}

// TestTransportOfDefaultsToP2P pins D-P2P-7: transport is p2p unless HA asks
// for the mothballed relay explicitly (missing or nonsense values included).
func TestTransportOfDefaultsToP2P(t *testing.T) {
	for _, in := range []string{"", "p2p", "P2P ", "bogus"} {
		if got := transportOf(in); got != transportP2P {
			t.Fatalf("transportOf(%q) = %q, want %q", in, got, transportP2P)
		}
	}
	if got := transportOf(transportHARelays); got != transportHARelays {
		t.Fatalf("transportOf(harelays) = %q, want harelays", got)
	}
}

// TestPumpRoutesMicFramesToDirectLeg runs the real pump loop: with a live leg
// the frames must come out of the leg, not the voice turn (the turn is only a
// harelays fallback).
func TestPumpRoutesMicFramesToDirectLeg(t *testing.T) {
	i := testIntercom(func([]int16) {})
	i.setTransport(transportP2P)
	leg := &fakeLeg{}
	i.setLeg(leg)
	if _, ok := i.session.CallerJoin(); !ok {
		t.Fatal("caller join refused")
	}
	frames := make(chan []int16)
	done := make(chan struct{})
	go func() { i.pump(frames, voice.Get().VS()); close(done) }()
	frame := make([]int16, p2pSamples)
	frames <- frame
	frames <- frame
	deadline := time.Now().Add(2 * time.Second)
	for leg.count() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	close(frames)
	<-done
	if leg.count() != 2 {
		t.Fatalf("leg received %d frames, want 2", leg.count())
	}
	if len(leg.frames[0]) != p2pSamples {
		t.Fatalf("forwarded frame = %d samples, want %d", len(leg.frames[0]), p2pSamples)
	}
}

// TestPumpDoesNotTransmitWhileRinging pins the anti-ambush rule against the
// new leg (D-DI-3): the callee's offer can arrive before it accepts, so a leg
// may well exist while the session is still Ringing — and it must stay silent.
func TestPumpDoesNotTransmitWhileRinging(t *testing.T) {
	i := testIntercom(func([]int16) {})
	i.setTransport(transportP2P)
	leg := &fakeLeg{}
	i.setLeg(leg)
	if !i.session.Ring() {
		t.Fatal("ring refused")
	}
	frames := make(chan []int16)
	done := make(chan struct{})
	go func() { i.pump(frames, voice.Get().VS()); close(done) }()
	frames <- make([]int16, p2pSamples)
	time.Sleep(50 * time.Millisecond)
	close(frames)
	<-done
	if leg.count() != 0 {
		t.Fatalf("leg transmitted %d frames while ringing — anti-ambush violated", leg.count())
	}
}

// TestLegClosesOnSessionEnd pins the D-P2P-2 teardown: the direct leg dies with
// the session on every transition to Idle, and the existing releaseTurn path
// still runs alongside it.
func TestLegClosesOnSessionEnd(t *testing.T) {
	i := &Intercom{
		session: New(time.Minute),
		status:  &esphome.TextSensor{},
		play:    func([]int16) {},
		turnOps: func() {}, // stand in for EndAudio+StopTurn
	}
	i.wire()
	leg := &fakeLeg{}
	i.setLeg(leg)
	i.mu.Lock()
	i.turnOpen = true
	i.mu.Unlock()
	if !i.session.Ring() {
		t.Fatal("ring refused")
	}
	if !i.session.End("hangup") {
		t.Fatal("end refused")
	}
	if !leg.wasClosed() {
		t.Fatal("session end did not close the direct leg")
	}
	if i.activeLeg() != nil {
		t.Fatal("closed leg still published")
	}
}

// TestConnectWithoutPeerStartsNoLeg pins the callee/peerless shape: a connect
// with transport=p2p but no peer arg (the callee — per D-P2P-5 the caller
// always offers) creates no leg; the callee only ever answers. It also pins
// the D-P2P-8 fix's leg gate: on p2p the join opens no turn, and the leg start
// must not have keyed on turnOpen — the session joining IS the readiness.
func TestConnectWithoutPeerStartsNoLeg(t *testing.T) {
	i := &Intercom{session: New(time.Minute), status: &esphome.TextSensor{}, play: func([]int16) {}}
	i.vs = voice.Get().VS()
	i.setTransport(transportP2P)
	if !i.session.Ring() {
		t.Fatal("ring refused")
	}
	i.CallerConnectTo("di-1", "", transportP2P)
	if i.activeLeg() != nil {
		t.Fatal("peerless connect created a leg — the callee must only answer")
	}
	// The dot-native connect with a peer starts the leg from the LIVE session
	// alone: no turn exists on p2p and none may be required.
	i.CallerConnectTo("di-1", "kitchen-dot.jagbains.org", transportP2P)
	deadline := time.Now().Add(3 * time.Second)
	for i.activeLeg() == nil && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	leg, ok := i.activeLeg().(*dotLeg)
	if !ok || leg == nil {
		t.Fatal("peer connect did not start the caller leg (turn gate must not block p2p)")
	}
	i.clearLeg()
}

// TestPlayIntercomPushConverts pins the F1 fix at the wiring site (review of
// card t_be5293f6): the sink the leg is handed — playback() with no injected
// sink — must be playIntercom, and playIntercom must hand the frame to the
// lane through the converting push (speaker.PlayMono), never the raw
// PlayStream path that put 320 samples into 320 of the codec period's 2048
// slots. The frame's arrival format is pinned here (the leg's decoded 16 kHz
// mono 320-sample frame, exactly what the caller hands over); the 320 → 1920
// slot conversion is pinned by speaker's TestPlayMonoFillsTheCodecPeriod.
func TestPlayIntercomPushConverts(t *testing.T) {
	var lane config.Stream
	var got []int16
	old := playLane
	playLane = func(s config.Stream, mono []int16) {
		lane, got = s, append([]int16(nil), mono...)
	}
	defer func() { playLane = old }()

	i := &Intercom{}
	frame := make([]int16, p2pSamples)
	for n := range frame {
		frame[n] = 1000
	}
	i.playback()(frame) // what newDotLeg receives as its play func

	if lane != config.StreamIntercom {
		t.Fatalf("lane = %q, want %q", lane, config.StreamIntercom)
	}
	if len(got) != p2pSamples {
		t.Fatalf("lane push got %d samples, want the decoded %d", len(got), p2pSamples)
	}
	// An injected sink still wins (the pump/leg tests depend on it), so the
	// converting lane push is the production default only.
	before := len(got)
	injected := false
	i.play = func([]int16) { injected = true }
	i.playback()(frame)
	if !injected || len(got) != before {
		t.Fatal("an injected playback sink must bypass the speaker lane")
	}
}
