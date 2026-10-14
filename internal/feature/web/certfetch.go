package web

// D-CERT (specs/dot-cert-decisions.md, D-CERT-4/6): the dot-side cert
// fetcher. A goroutine owned by the tlsListener fetches the AES-GCM-encrypted
// wildcard bundle hermes stages on HA, decrypts it with the shared cert_key
// setting, validates it (parses as a keypair, covers this dot's derived
// hostname and the wildcard domain, sane expiry), atomically persists it to
// the state dir, and hot-swaps the listener's atomic cert. EVERY failure
// path keeps serving whatever is currently loaded (self-signed at worst):
// the loop logs and retries, it never crashes echod or tears down TLS.
//
// The fetch failing on every boot while the staging bundle doesn't exist yet
// is EXPECTED (404/403/connect-refused); the daily heartbeat notices when
// hermes' deploy hook first lands the bundle.

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ygelfand/echolocal/internal/config"
	"github.com/ygelfand/echolocal/internal/layout"
)

// StagedBundleURL is where hermes stages the encrypted cert bundle on HA
// (D-CERT-2: HA is a dumb staging bucket). Var so tests can redirect it.
// PORT NOTE: this must be :8123 (HA core's own TLS, *.jagbains.org cert) —
// the bare https://homeassistant.jagbains.org :443 listener is NPM and has
// no proxy host for that name, so SNI there fails "unrecognized name".
var StagedBundleURL = "https://homeassistant.jagbains.org:8123/local/dot-cert/bundle.bin"

// Bundle layout (must match hermes' deploy hook, card 2):
// base64( nonce[12] || AES-256-GCM( combined cert+key PEM ) ), key =
// SHA-256(passphrase). Nonce travels in the clear; GCM's tag authenticates.
const (
	bundleMaxSize = 1 << 20 // 1 MiB cap on the staged download

	// D-CERT-4: refetch this long before expiry, plus a daily heartbeat
	// that both re-checks staging and covers the boot-before-expiry case.
	certRenewLead = 7 * 24 * time.Hour
	certHeartbeat = 24 * time.Hour
	certMinValid  = 48 * time.Hour // sanity: staged cert must outlive 48h
	fetchTimeout  = 30 * time.Second
	minWait       = time.Minute // never spin on a fast-failing fetch
)

// atomicCert is the currently-served certificate: swapped by the fetcher,
// read by the GetCertificate callback on every handshake.
var atomicCert atomic.Value

func swapCert(c tls.Certificate) { atomicCert.Store(c) }

func currentCert() (*tls.Certificate, error) {
	c, ok := atomicCert.Load().(tls.Certificate)
	if !ok || len(c.Certificate) == 0 {
		return nil, errors.New("tls: no certificate loaded")
	}
	return &c, nil
}

// fetchLoop implements D-CERT-4: one fetch at boot, then a daily heartbeat;
// when the loaded cert is inside the renew-lead window the wait shrinks so
// the swap happens before expiry. Every error is logged and absorbed —
// fallback (currently loaded cert) is never lost, per D-CERT-6.
func fetchLoop(ctx context.Context) {
	fire := func() {
		if err := fetchAndApplyCert(ctx); err != nil {
			slog.Error("certfetch:", "err", err)
		}
	}
	fire()
	for {
		wait := certHeartbeat
		if leaf := loadedLeaf(); leaf != nil {
			if until := time.Until(leaf.NotAfter) - certRenewLead; until < wait {
				wait = until
			}
		}
		if wait < minWait {
			wait = minWait
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
			fire()
		}
	}
}

func loadedLeaf() *x509.Certificate {
	c, ok := atomicCert.Load().(tls.Certificate)
	if !ok || len(c.Certificate) == 0 {
		return nil
	}
	if c.Leaf != nil {
		return c.Leaf
	}
	leaf, err := x509.ParseCertificate(c.Certificate[0])
	if err != nil {
		return nil
	}
	return leaf
}

// fetchAndApplyCert is one full attempt: download, decrypt, validate,
// persist, swap. Any failure returns an error (fetchLoop logs it) and leaves
// the currently-served cert untouched.
func fetchAndApplyCert(ctx context.Context) error {
	key := config.Get().Intercom.CertKey
	if key == "" {
		return errors.New("no cert_key setting; fetch disabled")
	}

	bundle, err := downloadBundle(ctx)
	if err != nil {
		return err
	}
	plain, err := decryptBundle(bundle, key)
	if err != nil {
		return err
	}
	cert, err := validateFetchedCert(plain)
	if err != nil {
		return err
	}

	if err := persistCertPair(plain); err != nil {
		return fmt.Errorf("persist: %w", err)
	}
	swapCert(cert)
	slog.Info("certfetch: hot-swapped to fetched wildcard cert",
		"expiry", cert.Leaf.NotAfter.Format(time.RFC3339))
	return nil
}

func downloadBundle(ctx context.Context) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, fetchTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, StagedBundleURL, nil)
	if err != nil {
		return nil, fmt.Errorf("request build: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		// 404/connect-refused while staging is not deployed yet is the
		// expected state; logged as the plain failure it is.
		return nil, fmt.Errorf("staging unreachable: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("staging returned %s", resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, bundleMaxSize))
	if err != nil {
		return nil, fmt.Errorf("read staging: %w", err)
	}
	return b, nil
}

// decryptBundle AES-GCM-decrypts the staged bundle: base64(nonce||ciphertext),
// key = SHA-256(passphrase). Must match card 2's hermes-side encryptor.
func decryptBundle(bundle []byte, key string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(bundle)))
	if err != nil {
		return nil, fmt.Errorf("bundle is not base64: %w", err)
	}
	block, err := aes.NewCipher(deriveKey(key))
	if err != nil {
		return nil, fmt.Errorf("cipher: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("gcm: %w", err)
	}
	if len(raw) < gcm.NonceSize()+gcm.Overhead() {
		return nil, errors.New("bundle shorter than nonce+ciphertext")
	}
	plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], nil)
	if err != nil {
		return nil, errors.New("decrypt failed (wrong cert_key or corrupted bundle)")
	}
	return plain, nil
}

// deriveKey folds the shared passphrase to a 32-byte AES-256 key. Fixed
// SHA-256 — hermes' encryptor (card 2) must use the same.
func deriveKey(key string) []byte {
	sum := sha256.Sum256([]byte(key))
	return sum[:]
}

// validateFetchedCert enforces the decrypted bundle is a usable wildcard
// pair: parses via tls.LoadX509KeyPair, covers this dot's derived hostname,
// and has sane validity. D-CERT-5: hostname mismatch is LOUD — the caller
// logs it at error and keeps retrying; it is never silently downgraded to
// a self-signed cert while a fetched cert is present-but-wrong.
func validateFetchedCert(pemBytes []byte) (tls.Certificate, error) {
	cert, err := tls.X509KeyPair(pemBytes, pemBytes)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("parse pair: %w", err)
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("parse leaf: %w", err)
	}
	host := dotHostname()
	if err := leaf.VerifyHostname(host); err != nil {
		return tls.Certificate{}, fmt.Errorf(
			"HOSTNAME MISMATCH (D-CERT-5): staged cert covers %v (issued by %q), this dot needs %q",
			hostnameList(leaf), leaf.Issuer.CommonName, host)
	}
	now := time.Now()
	if now.Before(leaf.NotBefore) {
		return tls.Certificate{}, fmt.Errorf("staged cert not yet valid until %s",
			leaf.NotBefore.Format(time.RFC3339))
	}
	if until := leaf.NotAfter.Sub(now); until < certMinValid {
		return tls.Certificate{}, fmt.Errorf("staged cert expiry too close (%s left)",
			until.Truncate(time.Second))
	}
	cert.Leaf = leaf
	return cert, nil
}

func hostnameList(leaf *x509.Certificate) []string {
	if len(leaf.DNSNames) > 0 {
		return leaf.DNSNames
	}
	return []string{leaf.Subject.CommonName}
}

// dotHostname = install --name + "-dot." + wildcard domain (D-CERT-5).
// Var so tests can redirect (no device identity in unit-test envs).
var dotHostname = func() string {
	name := "echolocal"
	if slug := layout.Slug(config.Get().Device.Name); slug != "" {
		name = slug
	}
	return name + "-dot." + config.CertHostDomain
}

// persistCertPair writes the decrypted PEM to the state dir: cert and key
// separated out and each replaced atomically (temp file + rename), so a
// crash mid-write never leaves a half pair that would fail keypair loading
// on the next boot.
func persistCertPair(pemBytes []byte) error {
	certPEM, keyPEM := splitPEM(pemBytes)
	if len(certPEM) == 0 || len(keyPEM) == 0 {
		return errors.New("decrypted bundle missing cert or key PEM block")
	}
	if err := atomicWrite(certPath, certPEM); err != nil {
		return err
	}
	return atomicWrite(keyPath, keyPEM)
}

func splitPEM(pemBytes []byte) ([]byte, []byte) {
	var certPEM, keyPEM bytes.Buffer
	rest := pemBytes
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		switch {
		case strings.Contains(block.Type, "CERTIFICATE"):
			pem.Encode(&certPEM, block)
		case strings.Contains(block.Type, "PRIVATE KEY"):
			pem.Encode(&keyPEM, block)
		}
	}
	return certPEM.Bytes(), keyPEM.Bytes()
}

func atomicWrite(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, filepath.Base(path)+".fetch-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	// write + fsync so a power loss between rename and disk never strands a
	// truncated file under the real name
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	return nil
}
