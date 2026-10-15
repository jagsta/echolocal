package intercom

// D-P2P (specs/dot-p2p-decisions.md, commit a55be20): dot↔dot intercom media
// goes DIRECT over pion WebRTC. Home Assistant stays in the SIGNALLING loop
// (ring/accept/hang-up/permissions, D-P2P-2) and the HA-side frame relay is
// mothballed behind the connect action's transport arg (D-P2P-7).
//
// Decision map for this file:
//   - D-P2P-1: dot↔dot ONLY. The phone leg (internal/feature/call, libcountertop
//     pkg/media/call) is untouched — separate connections, separate tracks.
//   - D-P2P-4: pion WebRTC with SDP over HTTPS :443. The card-14 ICE UDP arming
//     (feature/call armICEPorts → iptables ECHOLOCAL_CALLUDP 32768:60999) is
//     reused as-is: it is a firewall window, not a libcountertop API.
//   - D-P2P-5: ICE = host candidates only (no STUN/TURN); the CALLER always
//     offers and the callee answers inline over HTTP.
//   - D-P2P-6: this leg runs 16kHz MONO Opus — the mic.Source native rate
//     (hardware/mic Rate 16000, FrameSamples 320 = 20ms), so the capture and
//     encode halves cost armv7 no resample CPU. The phone path stays 48k
//     stereo on its own connection. The INBOUND half is converted to the
//     codec's 48k stereo layout at the speaker seam (speaker.PlayMono), not
//     here — see the correction note in specs/dot-p2p-decisions.md D-P2P-6.
//   - D-P2P-8: nothing here touches wake suppression (detect.PauseWhen,
//     voice.SetGuard) — and once the leg is live, no call audio enters the
//     ESPHome voice turn at all, so the assistant-hijack failure mode cannot
//     recur (see wiring.pump).
//
// The libcountertop PeerConnection helper (pkg/media/rtc.Link) is deliberately
// NOT reused: its seam hard-codes the phone leg's 48k/stereo capability,
// 48000-rate decoder and video/screen side-channels (rtc.go PlayRate 48000,
// PlayChannels 2, api() registering Opus 48000/2), and the fork ban on
// external modules (card constraint) means it cannot grow a 16k-mono mode.
// What IS reused is the machinery it wraps: pion/webrtc with an explicit
// MediaEngine + host-only setting engine, pion/opus encode/decode, and the
// same gather-into-SDP offer/answer shape (rtc.Link.Offer/Accept/Answered).

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/interceptor"
	"github.com/pion/opus"
	"github.com/pion/webrtc/v4"
	"github.com/pion/webrtc/v4/pkg/media"
)

const (
	// p2pSamples is one dot↔dot Opus frame: 20ms at 16kHz mono, exactly the
	// mic fan-out's native frame (mic.FrameSamples = mic.Rate/50 = 320).
	p2pSamples = 320
	// p2pFrame is that frame's RTP duration.
	p2pFrame = 20 * time.Millisecond
	// p2pMaxPayload is the Opus packet budget (rtc.go's 1275-byte RTP MTU).
	p2pMaxPayload = 1275
	// p2pMaxSamples is the decoder scratch size: the longest Opus frame
	// (120ms) at the highest rate we might be handed.
	p2pMaxSamples = 5760

	// D-P2P-6: the dot↔dot track's clock rate and channel count. 16kHz MONO.
	p2pClockRate = 16000
	p2pChannels  = 1

	// p2pPayloadType is the same dynamic PT the phone leg uses for Opus
	// (rtc.go opusType) — no negotiation dependency, just familiarity.
	p2pPayloadType = 111

	// p2pGatherTimeout bounds ICE gathering so a connect can never block the
	// action worker or the HTTP handler indefinitely.
	p2pGatherTimeout = 5 * time.Second
)

// p2pCodec is the dot↔dot track's RTP capability (D-P2P-6). Both endpoints
// register exactly this, which is what keeps 16k mono from ever negotiating
// down to (or up from) the phone leg's 48k stereo. The values stay on
// capability lines of their own — naming them through constants would hide
// the decision behind an indirection — and p2pClockRate / p2pChannels (the
// encoder's and decoder's view) are pinned to them by TestP2PCodecIs16kMono.
func p2pCodec() webrtc.RTPCodecCapability {
	return webrtc.RTPCodecCapability{
		MimeType: webrtc.MimeTypeOpus,

		ClockRate: 16000,
		Channels:  1,

		SDPFmtpLine: "minptime=10;useinbandfec=1",
	}
}

// p2pConfig is the PeerConnection configuration for this leg (D-P2P-5):
// OnlyCandidateTypeHost. ICEServers is deliberately EMPTY — with no STUN or
// TURN server configured pion never gathers server-reflexive or relayed
// candidates, so the pair can only connect over the LAN addresses that
// UDR/mDNS discovery already yielded for both dots (verified live 15 Oct:
// dual peer-found addrs on both dots, and kitchen-dot.jagbains.org resolving
// on the LAN). This is also why no firewall work is needed beyond the
// already-armed ECHOLOCAL_CALLUDP accept chain: host↔host checks stay inside
// it.
func p2pConfig() webrtc.Configuration {
	return webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{}, // empty ICEServers ⇒ host candidates only
	}
}

// p2pSettingEngine mirrors the validated phone leg's network posture
// (rtc.go api()): multicast-DNS disabled and UDP4 only, so the candidate set
// is plain IPv4 host addresses on the LAN.
func p2pSettingEngine() webrtc.SettingEngine {
	var se webrtc.SettingEngine
	se.SetICEMulticastDNSMode(ice.MulticastDNSModeDisabled)
	se.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	return se
}

const (
	// transportP2P is the D-P2P-7 default: dot↔dot media goes direct (D-P2P-4).
	transportP2P = "p2p"
	// transportHARelays is the MOTHBALLED v1 path (D-DI-4's HA frame relay):
	// relay.py + _send_frame + their tests stay alive, unreached at default.
	transportHARelays = "harelays"
)

// transportOf normalises the dropin_dial/dropin_connect transport arg: any
// value other than the mothballed relay selects the direct leg (D-P2P-7,
// default p2p).
func transportOf(v string) string {
	if v == transportHARelays {
		return transportHARelays
	}
	return transportP2P
}

// mediaLeg is the direct (or, in tests, fake) media leg a live session owns.
// The pump routes captured mic frames into it and teardown closes it.
type mediaLeg interface {
	// Send encodes and transmits one 20ms 16kHz-mono frame.
	Send(pcm []int16) error
	// Close tears the PeerConnection down exactly once.
	Close() error
}

// dotLeg is one dot↔dot pion session: an Opus 16k-mono send track fed from
// the intercom mic listener and a receive path that decodes inbound RTP onto
// the config.StreamIntercom speaker lane.
type dotLeg struct {
	pc    *webrtc.PeerConnection
	track *webrtc.TrackLocalStaticSample
	enc   *opus.Encoder
	play  func([]int16)

	sent atomic.Uint64
	recv atomic.Uint64

	closed sync.Once
	state  atomic.Value // webrtc.PeerConnectionState (for logs/tests)
}

var errLegClosed = errors.New("intercom: direct leg closed")

// newDotLeg builds the PeerConnection, track and encoder (D-P2P-4/6) but does
// no signalling: the caller calls Offer, the callee calls Answer.
func newDotLeg(play func([]int16)) (*dotLeg, error) {
	if play == nil {
		return nil, errors.New("intercom: direct leg needs a playback sink")
	}
	m := &webrtc.MediaEngine{}
	if err := m.RegisterCodec(
		webrtc.RTPCodecParameters{RTPCodecCapability: p2pCodec(), PayloadType: p2pPayloadType},
		webrtc.RTPCodecTypeAudio,
	); err != nil {
		return nil, fmt.Errorf("dot leg codec: %w", err)
	}
	reg := &interceptor.Registry{}
	if err := webrtc.RegisterDefaultInterceptors(m, reg); err != nil {
		return nil, fmt.Errorf("dot leg interceptors: %w", err)
	}
	se := p2pSettingEngine()
	api := webrtc.NewAPI(
		webrtc.WithMediaEngine(m),
		webrtc.WithInterceptorRegistry(reg),
		webrtc.WithSettingEngine(se),
	)
	pc, err := api.NewPeerConnection(p2pConfig())
	if err != nil {
		return nil, fmt.Errorf("dot leg peer connection: %w", err)
	}
	track, err := webrtc.NewTrackLocalStaticSample(p2pCodec(), "intercom", "dotintercom")
	if err != nil {
		_ = pc.Close()
		return nil, fmt.Errorf("dot leg track: %w", err)
	}
	sender, err := pc.AddTrack(track)
	if err != nil {
		_ = pc.Close()
		return nil, fmt.Errorf("dot leg add track: %w", err)
	}
	enc, err := opus.NewEncoder(opus.WithChannels(p2pChannels), opus.WithApplication(opus.ApplicationVoIP))
	if err != nil {
		_ = pc.Close()
		return nil, fmt.Errorf("dot leg encoder: %w", err)
	}
	l := &dotLeg{pc: pc, track: track, enc: enc, play: play}
	l.state.Store(webrtc.PeerConnectionStateNew)
	go l.drain(sender) // RTCP/interceptor reader; the sender must be read
	pc.OnTrack(func(t *webrtc.TrackRemote, _ *webrtc.RTPReceiver) { go l.receive(t) })
	pc.OnConnectionStateChange(func(s webrtc.PeerConnectionState) {
		l.state.Store(s)
		slog.Info("dot leg state", "state", s.String(), "sent", l.sent.Load(), "recv", l.recv.Load())
	})
	return l, nil
}

// State reports the last observed PeerConnection state.
func (l *dotLeg) State() webrtc.PeerConnectionState {
	s, _ := l.state.Load().(webrtc.PeerConnectionState)
	return s
}

// Sent/Recv are the encoded-out and decoded-in frame counters (evidence for
// the deploy card's live verification).
func (l *dotLeg) Sent() uint64 { return l.sent.Load() }
func (l *dotLeg) Recv() uint64 { return l.recv.Load() }

// Offer builds the caller's SDP with host candidates (D-P2P-5).
func (l *dotLeg) Offer(ctx context.Context) (string, error) {
	offer, err := l.pc.CreateOffer(nil)
	if err != nil {
		return "", fmt.Errorf("dot leg offer: %w", err)
	}
	return l.settle(ctx, offer)
}

// Answer accepts the caller's offer and returns this dot's SDP (D-P2P-4:
// delivered inline in the HTTP response, the card-13 loopback shape).
func (l *dotLeg) Answer(ctx context.Context, offer string) (string, error) {
	if err := l.pc.SetRemoteDescription(
		webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offer},
	); err != nil {
		return "", fmt.Errorf("dot leg remote offer: %w", err)
	}
	answer, err := l.pc.CreateAnswer(nil)
	if err != nil {
		return "", fmt.Errorf("dot leg answer: %w", err)
	}
	return l.settle(ctx, answer)
}

// Answered hands the caller the callee's SDP.
func (l *dotLeg) Answered(answer string) error {
	if err := l.pc.SetRemoteDescription(
		webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: answer},
	); err != nil {
		return fmt.Errorf("dot leg remote answer: %w", err)
	}
	return nil
}

// settle sets the local description and waits for ICE gathering to finish so
// the SDP handed to the peer carries every host candidate (same shape as
// rtc.Link.settle — one round trip, no trickle ICE).
func (l *dotLeg) settle(ctx context.Context, desc webrtc.SessionDescription) (string, error) {
	gathered := webrtc.GatheringCompletePromise(l.pc)
	if err := l.pc.SetLocalDescription(desc); err != nil {
		return "", fmt.Errorf("dot leg local description: %w", err)
	}
	select {
	case <-gathered:
	case <-ctx.Done():
		return "", fmt.Errorf("dot leg gather: %w", ctx.Err())
	}
	local := l.pc.LocalDescription()
	if local == nil {
		return "", errors.New("dot leg: no local description")
	}
	return local.SDP, nil
}

// Send encodes one mic frame (16k mono, 20ms) onto the track. Called from the
// intercom pump goroutine — one frame in, one Opus packet out, no queueing:
// the mic fan-out already paces frames, and a slow leg must not stall it.
func (l *dotLeg) Send(pcm []int16) error {
	if len(pcm) != p2pSamples {
		return fmt.Errorf("dot leg: %d samples, want %d", len(pcm), p2pSamples)
	}
	if l.State() == webrtc.PeerConnectionStateClosed {
		return errLegClosed
	}
	out := make([]byte, p2pMaxPayload)
	n, err := l.enc.EncodeSILK(pcm, opus.BandwidthWideband, out)
	if err != nil {
		return fmt.Errorf("dot leg encode: %w", err)
	}
	l.sent.Add(1)
	return l.track.WriteSample(media.Sample{
		Data:     append([]byte(nil), out[:n]...),
		Duration: p2pFrame,
	})
}

// drain reads the sender so pion's interceptors and RTCP feedback run; the
// content is unused on the audio-only leg.
func (l *dotLeg) drain(sender *webrtc.RTPSender) {
	buf := make([]byte, p2pMaxPayload)
	for {
		if _, _, err := sender.Read(buf); err != nil {
			return
		}
	}
}

// receive decodes inbound RTP onto the intercom speaker lane (D-P2P-6:
// 16kHz mono on the wire, exactly the mic frame format both dots encode).
// The lane itself is 48kHz stereo, so the sink (wiring.playIntercom →
// speaker.PlayMono) owns the resample/interleave.
func (l *dotLeg) receive(t *webrtc.TrackRemote) {
	dec, err := opus.NewDecoderWithOutput(p2pClockRate, p2pChannels)
	if err != nil {
		slog.Error("dot leg decoder", "err", err)
		return
	}
	out := make([]int16, p2pMaxSamples*p2pChannels)
	for {
		p, _, err := t.ReadRTP()
		if err != nil {
			return
		}
		n, err := dec.DecodeToInt16(p.Payload, out)
		if err != nil || n <= 0 {
			slog.Debug("dot leg decode", "err", err)
			continue
		}
		l.recv.Add(1)
		l.play(append([]int16(nil), out[:n*p2pChannels]...))
	}
}

// Close tears the PeerConnection down exactly once.
func (l *dotLeg) Close() error {
	var err error
	l.closed.Do(func() {
		l.state.Store(webrtc.PeerConnectionStateClosed)
		err = l.pc.Close()
		slog.Info("dot leg closed", "sent", l.sent.Load(), "recv", l.recv.Load())
	})
	return err
}

// dotOfferMsg is the D-P2P-4 signalling envelope: SDP over HTTPS :443, one
// POST and one inline answer (D-P2P-5), no second round-trip. Call carries
// the HA session id so both dots' logs join up with HA's.
type dotOfferMsg struct {
	Call      string `json:"call"`
	From      string `json:"from,omitempty"`
	Transport string `json:"transport,omitempty"`
	SDP       string `json:"sdp"`
}

func (m dotOfferMsg) encode() ([]byte, error) { return json.Marshal(m) }
