# Decision D-CERT: dot-pulled Let's Encrypt wildcard certs (removes NPM from call path)

Grilled with Jag 14 Oct 2026. Status: DECIDED, not yet implemented.
Background: fetch() cannot accept self-signed certs cross-origin (Jag-verified,
card 16 family), so every dot needs a genuinely-trusted TLS origin to receive
phone call offers. The NPM proxy was the v1 answer; per-dot proxy hosts scale
badly. Jag's `*.jagbains.org` wildcard LE cert + UniFi local DNS records
(`kitchen-dot.jagbains.org` etc.) make direct dot TLS feasible.

## Decisions

- **D-CERT-1: ciphertext at rest on HA, shared symmetric key.** hermes
  encrypts the cert bundle (AES-GCM) before pushing it to HA; each dot holds
  the shared passphrase in its own state (via existing `ctl settings`
  mechanism). Per-dot keys rejected: one shared wildcard to steal, per-dot
  keys only protect per-dot copies and multiply management for zero real
  gain. Rationale: HA is the weakest link — most reachable, most clients —
  so key material must never sit plaintext in `/config/www/`.
- **D-CERT-2: hermes owns issuance/encryption/push; HA is a dumb staging
  bucket.** Renewal cron on hermes (existing daily-cron pattern), deploy hook
  encrypts and scp's to `root@192.168.10.83`. No certbot machinery inside
  HA's containers (would need Cloudflare token inside HA — worse).
- **D-CERT-3: unauthenticated GET of the ciphertext.** Boundary = the shared
  key, not the download. An attacker reading the bundle learns nothing
  without the dot-side key. No HA-side auth middleware; keeps the fetch a
  plain HTTPS GET (bonus: key doesn't appear in HA access logs, which a
  URL-token variant would cause).
- **D-CERT-4: fetch trigger = boot + expiry-driven + daily heartbeat +
  manual force-refetch.** On boot the dot fetches immediately; schedules the
  next fetch ~7 days before the staged cert's expiry; a daily heartbeat
  verifies the staged bundle still exists / parses / validates, catching a
  deleted-staging outage before it becomes one. `ctl` escape hatch
  (`force-refetch`) for testing and recovery.
- **D-CERT-5: dot hostname = `install --name` + `-dot.jagbains.org`
  (derived, not a setting).** One flag drives everything: room name → device
  name → area → FQDN → DNS record → tile. echod SAN-checks the fetched cert
  against its own derived hostname and fails LOUDLY on mismatch (silent
  hostname disagreements cost cards 13/14 real time — never repeat that
  class). `web.hostname` may exist later as a rarely-used override; not the
  primary path.
- **D-CERT-6: conservatism on cleanup.** NPM's per-dot proxy hosts
  (`kitchen.jagbains.org`, `lounge.jagbains.org`) STAY but dormant — an
  instant fallback to the working v1 path (point a tile's `sig=` back at one
  and the tiles work again with zero code). Removed only after a full
  cross-room test passes on dot-pulled certs. Self-signed generation in
  `internal/feature/web/tls.go` is RETAINED as the zero-state fallback so a
  fresh dot boots to working (if untrusted) TLS before its first successful
  fetch; the fetch upgrades it in place.

## Implementation sketch (for the loop)

Cert flow: certbot on hermes with Cloudflare DNS plugin →
`jagbains.org` + `*.jagbains.org` → encrypt bundle with shared key →
scp to HA staging URL path → dots fetch over HTTPS from
`homeassistant.jagbains.org` → verify (parse, `*.jagbains.org` SAN, dot's
hostname matches, expiry sane) → atomic write to `/data/misc/echolocal/
tls.pem`/`tls-key.pem` → **hot-swap** the listener cert.

Code crux (hot-swap): `Run()` in tls.go builds one `http.Server` with a
fixed `Certificates` slice. Change to `TLSConfig.GetCertificate` callback
reading an atomic value the fetcher updates — no listener restart needed,
standard pattern, clean seam. `ensureCertificate()` behavior preserved for
the fallback path (only generates when files absent).

Runbook after this lands (per dot):
1. DHCP reservation (UDR, MAC ↔ 192.168.10.x)
2. `echolocal install --name <room>` (drives hostname too)
3. UniFi local DNS record: `<room>-dot.jagbains.org` → dot IP
4. `ctl settings` shared decrypt key on the dot (one-liner)
5. adopt in HA, regenerate tiles with `sig=https://<room>-dot.jagbains.org`
6. NO NPM step at all.

`cors_origins` per dot must include the page origin
(`https://homeassistant.jagbains.org:8123`) AND each dot's own
`https://<room>-dot.jagbains.org` (tiles fetch the dot cross-origin).
Exact-match allowlist entries; fail-closed.

Open questions parked for implementation (not grilling blockers):
- shared-key passphrase charset/distribution (same Slack-DM convention as
  pairing keys; NOT in chat or committed files)
- heartbeat schedule constant + jitter (avoid fleet-wide thundering herd at
  6h intervals; trivial with ≤3 dots but design it in)
- whether echod should ALSO fetch a CA bundle update path or LE trust is
  sufficient (Go's stdlib x509 pool on the dot covers LE's intermediates;
  ISRG Root X1 is on Fire OS 6 — verify on dot, one-liner check)