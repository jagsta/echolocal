package call

// Card 7 (D-DI-8, specs/phone-dropin-webrtc.md): phone PWA pairing gate +
// loopback answer delivery.
//
// A phone/web caller posts POST /call/offer (and /call/end) to echod's web
// port. Upstream's handlers accept any caller; card 7 wraps them so that
// phone-sourced signalling (From = "phone:<name>", the card-6 convention)
// must carry a pairing key matching an entry of the intercom settings'
// phone_keys map (D-DI-9b: the matched entry's NAME becomes the caller
// identity, server-side; the body's from claim is ignored for phone
// sources). Fail-closed: when no keys are configured every phone offer is
// rejected. Dot-to-Dot calls (ids without the "phone:" prefix) hit the
// upstream handlers unwrapped — byte-identical acceptance.
//
// The upstream callee sends its answer SDP by POSTing /call/answer back to
// the CALLER's address (libcountertop pkg/media/call signal.go), which works
// Dot-to-Dot but not for a browser. The phone offer is therefore held open:
// the upstream handler runs as normal (Ring starts on the callee), and the
// callee's answer POST — which arrives at the dot's own /call/answer because
// card 6's ad-hoc phone peer resolves to the dot itself — is captured here
// and returned in the original /call/offer HTTP response.

import (
	"crypto/subtle"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	sharedcall "github.com/ygelfand/libcountertop/pkg/media/call"

	"github.com/ygelfand/echolocal/internal/config"
	"github.com/ygelfand/echolocal/internal/feature/web"
)

const phoneOfferWait = 60 * time.Second

type phoneEnvelope struct {
	Call    string `json:"call"`
	From    string `json:"from,omitempty"`
	Name    string `json:"name,omitempty"`
	Pairing string `json:"pairing,omitempty"`
	Reason  string `json:"reason,omitempty"`
	SDP     string `json:"sdp,omitempty"`
}

// phoneGate wraps the upstream call handlers with the phone pairing key
// check and the loopback answer capture.
type phoneGate struct {
	offer *sharedcall.Calls

	// card 13: injectable upstream handlers for tests; nil falls back to
	// the offer component's own handlers.
	upOffer  http.Handler
	upAnswer http.Handler

	mu      sync.Mutex
	pending map[string]chan phoneEnvelope // call id -> answer SDP waiter
}

func newPhoneGate(c *sharedcall.Calls) *phoneGate {
	return &phoneGate{offer: c, pending: map[string]chan phoneEnvelope{}}
}

// upstream resolves the handler the gate forwards to: an injected test
// double when set, the configured Calls component otherwise.
func (g *phoneGate) upstreamOffer() http.Handler {
	if g.upOffer != nil {
		return g.upOffer
	}
	return g.offer.OfferHandler()
}

func (g *phoneGate) upstreamAnswer() http.Handler {
	if g.upAnswer != nil {
		return g.upAnswer
	}
	return g.offer.AnswerHandler()
}

func (g *phoneGate) PhoneOfferHandler() http.Handler { return http.HandlerFunc(g.offerPhone) }
func (g *phoneGate) PhoneAnswerHandler() http.Handler {
	return http.HandlerFunc(g.answerPhone)
}
func (g *phoneGate) PhoneEndHandler() http.Handler { return http.HandlerFunc(g.endPhone) }

// phonePairing checks the envelope's key against EACH entry of the
// configured name->key map (D-DI-9b: key-as-identity). On a match it
// returns the matched NAME so the caller's From can be overwritten
// server-side — the body's from claim is ignored for phone sources (the
// presented key is the identity, anti-spoof). Fail-closed: an empty or
// absent map disables phone sources entirely.
func (g *phoneGate) phonePairing(m *phoneEnvelope) (name string, enabled, ok bool) {
	for n, want := range config.Get().Intercom.PhoneKeys {
		if want == "" {
			continue
		}
		if subtle.ConstantTimeCompare([]byte(m.Pairing), []byte(want)) == 1 {
			return n, true, true
		}
	}
	if len(config.Get().Intercom.PhoneKeys) == 0 {
		return "", false, false // fail-closed: pairing not configured, phone sources off
	}
	return "", true, false // pairing on, key missing or mismatched
}

// check permits non-phone callers and gates phone ones; it writes the
// response itself when a phone request is rejected. On success it forces
// the caller identity to the matched entry's name (D-DI-9b anti-spoof).
func (g *phoneGate) check(w http.ResponseWriter, m *phoneEnvelope) bool {
	if !IsPhoneSource(m.From) {
		return true
	}
	name, enabled, ok := g.phonePairing(m)
	switch {
	case !enabled:
		http.Error(w, "pairing disabled", http.StatusForbidden)
	case !ok:
		http.Error(w, "pairing missing or mismatched", http.StatusUnauthorized)
	}
	if enabled && ok {
		m.From = PhoneSourcePrefix + name
	}
	return enabled && ok
}

func (g *phoneGate) offerPhone(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	r.Body = io.NopCloser(&resetReader{data: body})
	var m phoneEnvelope
	_ = json.Unmarshal(body, &m)
	if !g.check(w, &m) {
		return
	}
	// Card 13: identity = the pairing key's name, stripped of the phone:
	// prefix for the upstream Name field (the matched entry's name IS the
	// caller identity, D-DI-9b).
	identity := m.From
	if IsPhoneSource(identity) {
		identity = PhoneDisplayName(identity)
	}
	if m.Call == "" || m.SDP == "" {
		// Malformed phone offers get upstream's own "bad offer" reply.
		r.Body = io.NopCloser(&resetReader{data: body})
		g.upstreamOffer().ServeHTTP(w, r)
		return
	}

	// Card 13: the gate reached here means the pairing key matched, so the
	// matched entry's name IS the caller identity. The body's from claim
	// (the PWA used to send the room display name — live finding: the dot
	// then matched a peer literally named "Kitchen" and rang from=Kitchen)
	// is discarded: the request is re-fed upstream with from/name forced to
	// the key-derived identity so the ring announces the caller, not a URL
	// parameter, and the callee's session targets no discoverable peer.
	m.From = PhoneSourcePrefix + identity
	m.Name = identity
	body, _ = json.Marshal(m)
	r.Body = io.NopCloser(&resetReader{data: body})

	// Card 13: force the answer loopback. Upstream offered() builds the
	// callee session's s.addr purely from r.RemoteAddr (a plain struct
	// field on the request this gate owns — writable before ServeHTTP), and
	// post() constructs "http://<addr>/call/answer" from it. Leaving the
	// remote address as the phone (or the NPM proxy) makes the callee POST
	// its answer SDP to a host that cannot receive it: the live call failed
	// with "step=answer err=status 404". Rewriting it to the dot's own web
	// listener (http://127.0.0.1:<web.Port>, the libwe Server on Port 80)
	// routes the callee's answer POST into this gate's answerPhone, which
	// captures it into the pending offer held below.
	r.RemoteAddr = net.JoinHostPort("127.0.0.1", strconv.Itoa(web.Port))

	// Hold the offer open: upstream rings the Dot now (its workers carry
	// the ring after the handler returns), and the callee's answer
	// arrives over the loopback.
	ch := make(chan phoneEnvelope, 1)
	g.mu.Lock()
	g.pending[m.Call] = ch
	g.mu.Unlock()
	defer func() {
		g.mu.Lock()
		if g.pending[m.Call] == ch {
			delete(g.pending, m.Call)
		}
		g.mu.Unlock()
	}()

	captured := &captureWriter{ResponseWriter: w}
	g.upstreamOffer().ServeHTTP(captured, r)
	if captured.status != http.StatusAccepted {
		// Upstream refused (busy, incoming off, malformed): replay its
		// status to the phone.
		w.WriteHeader(captured.status)
		return
	}
	select {
	case a := <-ch:
		out, _ := json.Marshal(phoneEnvelope{Call: a.Call, SDP: a.SDP})
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write(out)
	case <-time.After(phoneOfferWait):
		// Nobody answered: end the ring the way an unanswered Dot caller
		// would rather than ride out the callee's full ring window.
		g.offer.EndHandler().ServeHTTP(
			&captureWriter{},
			newEndRequest(m.Call, "unanswered"),
		)
		return
	}
}

func (g *phoneGate) answerPhone(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	var m phoneEnvelope
	_ = json.Unmarshal(body, &m)
	g.mu.Lock()
	var out chan phoneEnvelope
	if m.Call != "" && m.SDP != "" {
		if ch, waiting := g.pending[m.Call]; waiting {
			out = ch
			delete(g.pending, m.Call)
		}
	}
	g.mu.Unlock()
	if out != nil {
		select {
		case out <- phoneEnvelope{Call: m.Call, SDP: m.SDP}:
		default:
		}
		w.WriteHeader(http.StatusOK)
		return
	}
	// Not a phone session's loopback answer: Dot-to-Dot behaviour.
	r.Body = io.NopCloser(&resetReader{data: body})
	g.upstreamAnswer().ServeHTTP(w, r)
}

func (g *phoneGate) endPhone(w http.ResponseWriter, r *http.Request) {
	body, _ := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	var m phoneEnvelope
	_ = json.Unmarshal(body, &m)
	if !g.check(w, &m) {
		return
	}
	r.Body = io.NopCloser(&resetReader{data: body})
	g.offer.EndHandler().ServeHTTP(w, r)
}

func newEndRequest(call, reason string) *http.Request {
	body, _ := json.Marshal(phoneEnvelope{Call: call, Reason: reason})
	req, _ := http.NewRequest(http.MethodPost, "/call/end", &resetReader{data: body})
	return req
}

// captureWriter records the status the upstream handler wrote without
// forwarding it, so the gate can respond itself afterwards.
type captureWriter struct {
	http.ResponseWriter
	status int
}

func (w *captureWriter) WriteHeader(code int) { w.status = code }
func (w *captureWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}
	return len(b), nil
}

// resetReader lets the gate re-feed the request body it already decoded.
type resetReader struct {
	data []byte
	off  int
}

func (r *resetReader) Read(p []byte) (int, error) {
	if r.off >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.off:])
	r.off += n
	return n, nil
}
