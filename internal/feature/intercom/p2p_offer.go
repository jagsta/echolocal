package intercom

// D-P2P-4/5: the dot↔dot signalling hop. Signalling itself stays HA-owned
// (D-P2P-2) — this file only carries the SDP exchange that follows HA's
// connect event.
//
//   - callee: POST {dotOfferPath} on the dot's own web listener (mirrored on
//     :443 by feature/web's TLS listener, web.TLSPort). It gates, answers and
//     returns the SDP INLINE in the HTTP response.
//   - caller: always the offerer (D-P2P-5). It POSTs its offer to the callee's
//     https://<peer>:{TLSPort}, and takes the answer from that same response.
//
// The gate mirrors the phone path's pairing gate in internal/feature/call
// (phoneGate.check, D-DI-9b): fail-closed, decided by the session machine and
// not by anything the network asserts. There, a phone offer is refused unless
// the presented key matches a configured entry; here, an offer is refused
// unless this dot's OWN session machine says a call was authorized (it is
// ringing after HA's dropin_dial, or it is in a call opened by Accept /
// CallerJoin — the accepted route, which is HA's connect). Idle, ended or
// otherwise-unaccounted-for states get a 403 and no media leg.

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ygelfand/echolocal/internal/feature/web"
)

const (
	// dotOfferPath is the callee's auto-answer route (D-P2P-4/5).
	dotOfferPath = "/call/dotoffer"
	// p2pOfferTimeout bounds the caller's POST — SDP in, answer out.
	p2pOfferTimeout = 10 * time.Second
	// p2pBodyLimit caps a signalling body (SDPs are a few KB).
	p2pBodyLimit = 1 << 20
)

func init() {
	// Registered exactly like the phone path's routes (feature/call init):
	// lazily resolved through Get() so nothing is constructed at init time.
	web.Handle("POST "+dotOfferPath, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Get().answerPeerOffer(w, r)
	}))
}

// DotOfferHandler is the callee's auto-answer endpoint handler.
func (i *Intercom) DotOfferHandler() http.Handler {
	return http.HandlerFunc(i.answerPeerOffer)
}

// answerPeerOffer implements the callee half of D-P2P-4/5.
func (i *Intercom) answerPeerOffer(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, p2pBodyLimit))
	var m dotOfferMsg
	if err := json.Unmarshal(body, &m); err != nil || m.Call == "" || m.SDP == "" {
		http.Error(w, "bad offer", http.StatusBadRequest)
		return
	}
	// GATE (D-P2P-4/5): the session machine is the authority. HA's connect has
	// already authorized this call before the caller can offer — the caller
	// only learns the peer host from that connect — so a ringing dot (the
	// offer racing ahead of its own dropin_connect) and a dot already in an
	// accepted call both pass; everything else is refused fail-closed.
	if !i.session.OfferAuthorized(m.Call) {
		state := i.session.Now()
		slog.Warn("dot offer refused", "from", m.From, "session", m.Call, "state", string(state))
		http.Error(w, "refused: session is "+string(state), http.StatusForbidden)
		return
	}
	leg, err := newDotLeg(i.playback())
	if err != nil {
		slog.Error("dot offer leg failed", "session", m.Call, "from", m.From, "err", err)
		http.Error(w, "no media", http.StatusInternalServerError)
		return
	}
	// Publish before answering: the pump must route mic frames into the leg
	// the instant it exists, and teardown (session → Idle) is what closes it.
	i.setLeg(leg)
	ctx, cancel := context.WithTimeout(r.Context(), p2pGatherTimeout)
	defer cancel()
	answer, err := leg.Answer(ctx, m.SDP)
	if err != nil {
		i.clearLeg()
		slog.Error("dot offer answer failed", "session", m.Call, "from", m.From, "err", err)
		http.Error(w, "answer failed", http.StatusInternalServerError)
		return
	}
	slog.Info("dot offer answered", "session", m.Call, "from", m.From)
	out, _ := dotOfferMsg{Call: m.Call, Transport: transportP2P, SDP: answer}.encode()
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write(out)
}

// startCallerLeg is the OFFERER half of D-P2P-5: the caller dot builds the
// leg, gathers its host candidates, POSTs the offer to the callee's HTTPS
// :443 and applies the answer that comes back in the HTTP response.
//
// Runs on its own goroutine (the connect action returns immediately — the
// dots' chime/LED/session work must not wait on media). A failure ends the
// session with reason "dropped", mirroring wiring.Accept/CallerConnect's
// behaviour when the turn cannot be opened: a call with no media path is not
// a call, and the dots' end chime + LEDs tell the household so rather than
// leaving a silent "in-call".
func (i *Intercom) startCallerLeg(session, peer string) {
	leg, err := newDotLeg(i.playback())
	if err != nil {
		i.mediaFailed(session, peer, err)
		return
	}
	i.setLeg(leg)
	ctx, cancel := context.WithTimeout(context.Background(), p2pOfferTimeout)
	defer cancel()
	offer, err := leg.Offer(ctx)
	if err != nil {
		i.mediaFailed(session, peer, err)
		return
	}
	answer, err := i.postOfferFn()(ctx, peer, dotOfferMsg{
		Call:      session,
		From:      dotHostName(),
		Transport: transportP2P,
		SDP:       offer,
	})
	if err != nil {
		i.mediaFailed(session, peer, err)
		return
	}
	if err := leg.Answered(answer); err != nil {
		i.mediaFailed(session, peer, err)
		return
	}
	slog.Info("dot leg offered", "session", session, "peer", peer)
}

// mediaFailed logs and tears the call down (see startCallerLeg).
func (i *Intercom) mediaFailed(session, peer string, err error) {
	slog.Error("dot leg failed", "session", session, "peer", peer, "err", err)
	i.clearLeg()
	i.session.End("dropped")
}

// postOfferFn resolves the offer POST, injectable so the negotiation can be
// exercised in-process (tests) without a TLS listener.
func (i *Intercom) postOfferFn() func(ctx context.Context, peer string, m dotOfferMsg) (string, error) {
	if i.post != nil {
		return i.post
	}
	return postOffer
}

// postOffer POSTs an offer to a peer dot's HTTPS :443 and returns the answer
// SDP from the response body (D-P2P-4/5, one round trip).
func postOffer(ctx context.Context, peer string, m dotOfferMsg) (string, error) {
	body, err := m.encode()
	if err != nil {
		return "", err
	}
	url := "https://" + net.JoinHostPort(peer, strconv.Itoa(web.TLSPort)) + dotOfferPath
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := peerDotClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("dot offer to %s: %w", peer, err)
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, p2pBodyLimit))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("dot offer to %s: status %d: %s", peer, resp.StatusCode, strings.TrimSpace(string(out)))
	}
	var answer dotOfferMsg
	if err := json.Unmarshal(out, &answer); err != nil {
		return "", fmt.Errorf("dot offer from %s: bad answer: %w", peer, err)
	}
	if answer.SDP == "" {
		return "", fmt.Errorf("dot offer from %s: empty answer", peer)
	}
	return answer.SDP, nil
}

// peerDotClient is the TLS client for the caller→callee signalling hop.
//
// The dots' :443 listener serves the D-CERT staged bundle (feature/web/tls.go
// loads the staged wildcard cert), whose chain the caller dot has no CA trust
// anchor for — so verification is skipped on this LAN hop. The peer host is
// not attacker-controlled: it comes from HA's device_map (the same trusted
// signalling channel that authorized the call in the first place, D-P2P-2),
// and the leg carries intercom audio inside the house, not secrets. Tightening
// this to cert pinning belongs with the cert card, not the P2P prototype.
func peerDotClient() *http.Client {
	return &http.Client{
		Timeout: p2pOfferTimeout,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // LAN leg, staged cert, see doc above
		},
	}
}

// dotHostName identifies this dot in the offer envelope (logs on the callee).
func dotHostName() string {
	name, err := os.Hostname()
	if err != nil || name == "" {
		return "echod"
	}
	return name
}

// errNoPeer is returned when a p2p connect names no peer to offer to.
var errNoPeer = errors.New("intercom: p2p connect without a peer host")
