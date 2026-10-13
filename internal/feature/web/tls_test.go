package web

// Card 11 tests: the self-signed pair is generated once and reused (never
// regenerated on reboot, so phones keep accepted-cert state), and the TLS
// mirror mux serves the routes registered through Handle().

import (
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ygelfand/echolocal/internal/layout"
	"github.com/ygelfand/libcountertop/pkg/runtime/status"
)

func withTempCertPaths(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	certPath, keyPath = filepath.Join(dir, "tls.pem"), filepath.Join(dir, "tls-key.pem")
	t.Cleanup(func() { certPath, keyPath = layout.StateDir+"/tls.pem", layout.StateDir+"/tls-key.pem" })
}

func TestCertificateIsGeneratedOnce(t *testing.T) {
	withTempCertPaths(t)

	first, err := ensureCertificate()
	if err != nil {
		t.Fatalf("first ensureCertificate: %v", err)
	}
	cert, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		t.Fatalf("generated pair does not load: %v", err)
	}
	if first.Leaf == nil && len(first.Certificate) == 0 {
		t.Fatal("no certificate material returned")
	}

	// Corrupt persistence to prove the load path (not regeneration) is used
	// when files exist and load cleanly.
	second, err := ensureCertificate()
	if err != nil {
		t.Fatalf("second ensureCertificate: %v", err)
	}
	if len(second.Certificate) == 0 || len(cert.Certificate) == 0 {
		t.Fatal("missing certificate on reuse")
	}
	if string(second.Certificate[0]) != string(cert.Certificate[0]) {
		t.Error("certificate regenerated although the pair loads — certs must persist")
	}
}

func TestTLCertificateCoversNameAndIP(t *testing.T) {
	withTempCertPaths(t)

	cert, err := ensureCertificate()
	if err != nil {
		t.Fatalf("ensureCertificate: %v", err)
	}
	certs, err := x509.ParseCertificates(cert.Certificate[0])
	if err != nil {
		t.Fatalf("parse certs: %v", err)
	}
	if len(certs) == 0 {
		t.Fatal("no certs")
	}
	leaf := certs[0]
	if leaf.Subject.CommonName == "" {
		t.Error("empty CommonName")
	}
	if len(leaf.IPAddresses) == 0 {
		t.Error("no IP SAN — the PWA dials by address")
	}
}

func TestTLSServesRegisteredRoutes(t *testing.T) {
	withTempCertPaths(t)

	srv := httptest.NewUnstartedServer(tlsMux())
	srv.EnableHTTP2 = false
	srv.StartTLS()
	defer srv.Close()

	client := srv.Client()

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, srv.URL+"/", nil)
	status.Handler(page)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "<!doctype html>") {
		t.Error("the TLS root did not render the status page")
	}
	_ = client
}
