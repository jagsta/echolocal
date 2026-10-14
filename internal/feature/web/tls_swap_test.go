package web

// D-CERT card 18 tests: the hot-swap path. GetCertificate must serve the
// seeded cert, and after the fetcher swaps the atomic the NEXT handshake
// must serve the NEW cert — no listener restart, no stale cache. The
// network path is NOT tested here (staging bundle doesn't exist yet).

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net/http"
	"strings"
	"testing"
	"time"
)

// genPair makes a self-signed (cert, key) PEM pair for host.
func genPair(t *testing.T, host string, notAfter time.Time) (tls.Certificate, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("key: %v", err)
	}
	tmpl := x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: host},
		DNSNames:     []string{host},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     notAfter,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, &tmpl, &tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("cert: %v", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal key: %v", err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("X509KeyPair: %v", err)
	}
	return pair, append(append([]byte(nil), certPEM...), keyPEM...)
}

// dialPeerName does a real TLS handshake against srv and returns the leaf
// the server actually presented.
func dialPeerName(t *testing.T, srvURL string) *x509.Certificate {
	t.Helper()
	conn, err := tls.Dial("tcp", strings.TrimPrefix(srvURL, "https://"),
		&tls.Config{InsecureSkipVerify: true}) // self-signed test certs
	if err != nil {
		t.Fatalf("handshake: %v", err)
	}
	state := conn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		t.Fatal("server presented no certificate")
	}
	return state.PeerCertificates[0]
}

func TestHotSwapServesNewCertAfterAtomicSwap(t *testing.T) {
	t.Helper()
	dotHostname = func() string { return "kitchen-dot.jagbains.org" }
	old, _ := genPair(t, "kitchen-dot.jagbains.org", time.Now().AddDate(0, 0, 90))
	fresh, freshBundle := genPair(t, "kitchen-dot.jagbains.org", time.Now().AddDate(0, 0, 90))

	if _, err := validateFetchedCert(freshBundle); err != nil {
		t.Fatalf("validateFetchedCert rejected a well-formed wildcard-style pair: %v", err)
	}
	if err := old.Leaf.VerifyHostname("kitchen-dot.jagbains.org"); err != nil {
		t.Fatalf("sanity: generated cert should match its own host: %v", err)
	}

	swapCert(old)
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{
		GetCertificate: func(*tls.ClientHelloInfo) (*tls.Certificate, error) {
			return currentCert()
		},
	})
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go http.Serve(ln, tlsMux())
	defer ln.Close()
	srvURL := "https://" + ln.Addr().String()

	if got := dialPeerName(t, srvURL); !got.Equal(old.Leaf) {
		t.Fatalf("first handshake served %v, want the seeded cert", got.Subject)
	}

	// THE SWAP: fetcher replaces the atomic; next handshake serves fresh.
	swapCert(fresh)
	if got := dialPeerName(t, srvURL); !got.Equal(fresh.Leaf) {
		t.Fatalf("post-swap handshake served %v serial %s, want the NEW cert",
			got.Subject, got.SerialNumber)
	}
}

func TestValidateRejectsWrongHostLoudly(t *testing.T) {
	dotHostname = func() string { return "kitchen-dot.jagbains.org" }
	_, bundle := genPair(t, "wrongtenant.example.com", time.Now().AddDate(0, 0, 90))
	_, err := validateFetchedCert(bundle)
	if err == nil {
		t.Fatal("cert for an unrelated host must be rejected (D-CERT-5)")
	}
	if !strings.Contains(err.Error(), "HOSTNAME MISMATCH") {
		t.Errorf("rejection should be the loud D-CERT-5 mismatch, got: %v", err)
	}
}

func TestValidateRejectsShortLivedCert(t *testing.T) {
	dotHostname = func() string { return "kitchen-dot.jagbains.org" }
	_, bundle := genPair(t, "kitchen-dot.jagbains.org", time.Now().Add(24*time.Hour))
	if _, err := validateFetchedCert(bundle); err == nil {
		t.Fatal("cert expiring inside the 48h sanity window must be rejected")
	}
}

func TestHotSwapFallbackWhenNoCertLoaded(t *testing.T) {
	atomicCert.Store(tls.Certificate{}) // empty atomic
	if _, err := currentCert(); err == nil {
		t.Fatal("GetCertificate must fail loudly with nothing loaded, never serve an empty cert")
	}
}

var _ = http.StatusOK
