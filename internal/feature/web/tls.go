package web

// Card 11 (D-DI-9): HTTPS signalling on the dot.
//
// The HA-hosted PWA (https://homeassistant.…:8123/local/phone-dropin.html)
// is a secure origin, so Chromium blocks its plain-HTTP signalling POSTs to
// the dot (mixed content + CORS). This adds a SECOND listener on :443 that
// serves the same handler chain: every route registered through fork-local
// Handle() (the sole choke point — call's /call/* and configsync's /config/*
// both go through it) is re-registered onto a mirror mux the TLS server
// mounts, topped by the same status page handler libweb uses. Dot-to-Dot
// traffic stays on the plain listener; no upstream (libcountertop) edits.

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/ygelfand/libcountertop/pkg/network/address"
	"github.com/ygelfand/echolocal/internal/status"

	"github.com/ygelfand/echolocal/internal/android/firewall"
	"github.com/ygelfand/echolocal/internal/config"
	"github.com/ygelfand/echolocal/internal/layout"
)

const (
	TLSPort      = 443
	CertFilePath = layout.StateDir + "/tls.pem" // /data/misc/echolocal/tls.pem
	CertKeyPath  = layout.StateDir + "/tls-key.pem"
)

var (
	// Persisted self-signed pair lives under /data/misc/echolocal (layout.StateDir);
	// vars so tests can redirect to temp dirs.
	certPath = CertFilePath
	keyPath  = CertKeyPath
)

var tlsGet = sync.OnceValue(func() *tlsListener { return newTLSListener() })

// TLSGet returns the fork-local TLS listener component.
func TLSGet() *tlsListener { return tlsGet() }

type tlsListener struct{ mu sync.Mutex }

func newTLSListener() *tlsListener {
	if err := firewall.Open(firewall.Web, TLSPort); err != nil {
		slog.Error("opening the tls port failed", "port", TLSPort, "err", err)
	}
	return &tlsListener{}
}

func (t *tlsListener) Name() string { return "tls" }

// routes mirrors everything registered via Handle() so the TLS listener can
// serve the same mux the plain listener builds (libweb keeps its mux private
// to Server.Run, so we record instead of reaching in).
type routesT struct {
	mu      sync.Mutex
	entries []routeEntry
}

type routeEntry struct {
	pattern string
	h       http.Handler
}

var routes routesT

// Handle registers a route on BOTH listeners: libweb's server (unchanged,
// Dot-to-Dot path) and the TLS mirror.
func Handle(pattern string, h http.Handler) {
	wrapped := CORS(pattern, h)
	routes.mu.Lock()
	routes.entries = append(routes.entries, routeEntry{pattern, wrapped})
	routes.mu.Unlock()
	Get().Handle(pattern, wrapped)
	// Card 12 fix: method-prefixed patterns (e.g. "POST /call/offer") leave
	// CORS preflights (OPTIONS) unhandled — they fall through to the catch-all
	// and die as 404 before the middleware can answer. Mirror each wrapped
	// cors route with an OPTIONS twin so the SAME middleware serves it; the
	// middleware's method branch already answers 204 for allowed origins and
	// 403 for disallowed ones.
	if strings.HasPrefix(pattern, "POST ") && corsRoutes[pattern] {
		pre := "OPTIONS " + strings.TrimPrefix(pattern, "POST ")
		routes.mu.Lock()
		routes.entries = append(routes.entries, routeEntry{pre, wrapped})
		routes.mu.Unlock()
		Get().Handle(pre, wrapped)
	}
}

// mirror mux: same routes + the same root status page libweb mounts.
func tlsMux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.Handle("/", status.Handler(page))
	routes.mu.Lock()
	defer routes.mu.Unlock()
	for _, e := range routes.entries {
		mux.Handle(e.pattern, e.h)
	}
	return mux
}

// Run serves TLS on :443 for as long as the plain server runs.
func (t *tlsListener) Run(ctx context.Context) error {
	// D-CERT card 18: seed the atomic once at startup — the persisted pair if
	// one exists (a fetched wildcard from a previous run), self-signed
	// otherwise. Then serve with a GetCertificate callback so the certfetch
	// goroutine can hot-swap without a listener restart.
	seed, err := ensureCertificate()
	if err != nil {
		return fmt.Errorf("tls: certificate: %w", err)
	}
	swapCert(seed)
	go fetchLoop(ctx)

	t.mu.Lock()
	srv := &http.Server{
		Addr:              ":" + strconv.Itoa(TLSPort),
		Handler:           tlsMux(),
		ReadHeaderTimeout: 5 * time.Second,
		TLSConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			// D-CERT: dynamic cert source, consulted per handshake; the
			// static Certificates slice must stay empty for it to win.
			GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
				return currentCert()
			},
		},
	}
	t.mu.Unlock()

	defer func() {
		stop, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = srv.Shutdown(stop)
	}()

	err = srv.Serve(tls.NewListener(keepListening(TLSPort), srv.TLSConfig))
	if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

// keepListening re-arms the accept loop if the listener dies, so a transient
// accept error doesn't take TLS signalling down until reboot.
func keepListening(port int) net.Listener {
	return &relisten{port: port, l: listen(port)}
}

func listen(port int) net.Listener {
	l, err := net.Listen("tcp", ":"+strconv.Itoa(port))
	if err != nil {
		slog.Error("tls: listen failed; will retry", "port", port, "err", err)
	}
	return l
}

type relisten struct {
	port int
	mu   sync.Mutex
	l    net.Listener
}

func (r *relisten) ensure() net.Listener {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.l == nil {
		r.l = listen(r.port)
	}
	return r.l
}

func (r *relisten) Accept() (net.Conn, error) {
	for {
		l := r.ensure()
		if l == nil {
			time.Sleep(time.Second)
			continue
		}
		c, err := l.Accept()
		if err == nil {
			return c, nil
		}
		if errors.Is(err, net.ErrClosed) {
			r.mu.Lock()
			r.l = nil
			r.mu.Unlock()
			continue
		}
		_ = l.Close()
		r.mu.Lock()
		r.l = nil
		r.mu.Unlock()
	}
}

func (r *relisten) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.l == nil {
		return nil
	}
	err := r.l.Close()
	r.l = nil
	return err
}

func (r *relisten) Addr() net.Addr {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.l == nil {
		return nil
	}
	return r.l.Addr()
}

// ensureCertificate loads the persisted self-signed pair, generating it once
// at first boot if either file is missing. Regenerated only when absent —
// never on reboot, so phones keep their accepted-cert state.
func ensureCertificate() (tls.Certificate, error) {
	if cert, err := tls.LoadX509KeyPair(certPath, keyPath); err == nil {
		return cert, nil
	}

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("tls: key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("tls: serial: %w", err)
	}

	tmpl := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: deviceName()},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}
	if ip := deviceIP(); ip != nil {
		tmpl.IPAddresses = append(tmpl.IPAddresses, ip)
	}
	if host := deviceName(); host != "" {
		tmpl.DNSNames = append(tmpl.DNSNames, host)
	}

	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("tls: cert: %w", err)
	}

	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("tls: key marshal: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	if err := os.MkdirAll(filepath.Dir(certPath), 0o755); err != nil {
		return tls.Certificate{}, fmt.Errorf("tls: state dir: %w", err)
	}
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		return tls.Certificate{}, fmt.Errorf("tls: write cert: %w", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		return tls.Certificate{}, fmt.Errorf("tls: write key: %w", err)
	}

	return tls.X509KeyPair(certPEM, keyPEM)
}

var serialLimit = new(big.Int).Lsh(big.NewInt(1), 128) // 2^128, exclusive max

// deviceName is the slug the dot already uses as its hostname.
func deviceName() string {
	if slug := layout.Slug(config.Get().Device.Name); slug != "" {
		return slug
	}
	return "echolocal"
}

// deviceIP returns the dot's primary IPv4 address for the cert's SANs.
func deviceIP() net.IP {
	for _, ip := range address.Addresses() {
		if ip.To4() != nil {
			return ip
		}
	}
	return nil
}
