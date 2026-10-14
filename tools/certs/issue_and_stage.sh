#!/usr/bin/env bash
# D-CERT card 2 — issue (or renew) the *.jagbains.org wildcard via Cloudflare
# DNS, encrypt with the shared key (AES-256-GCM via aesgcm_bundle.py), stage on
# HA (root@192.168.10.83:/config/www/dot-cert/), verify sha256 readback.
# See specs/dot-cert-decisions.md (D-CERT-1/2/3).
set -euo pipefail

CERT_DOMAIN="jagbains.org"
CERT_NAME="${CERT_NAME:-jagbains.org}"          # certbot --cert-name
HA_TARGET="root@192.168.10.83"
HA_STAGE_DIR="/config/www/dot-cert"             # D-CERT-2: HA is a dumb staging bucket
REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
HERMES_ENV="${HERMES_ENV:-$HOME/.hermes/.env}"
KEY_FILE="${KEY_FILE:-$HOME/.hermes/keys/dot-cert-key}"
CRED_FILE="${CRED_FILE:-$HOME/.config/letsencrypt/cloudflare.ini}"
WORK_DIR="${WORK_DIR:-$(mktemp -d /tmp/dot-cert.XXXXXX)}"
AESGCM_HELPER="$REPO_DIR/tools/certs/aesgcm_bundle.py"

usage() {
  cat <<EOF
Usage: issue_and_stage.sh [--renew] [--dry-run] [-h|--help]

D-CERT card 2: certbot issuance + AES-GCM encrypt + HA staging pipeline.

Options:
  --renew     renewal path: runs \`certbot renew --cert-name $CERT_NAME\`
              instead of first-issuance certonly. (certbot's own deploy hook or
              timer may already cover renewals; this flag exists for manual
              runs and for the dot_cert_renew cron wrapper.)
  --dry-run   pass --dry-run to certbot (validates the challenge path end-to-end
              against the LE staging CA). Still requires CLOUDFLARE_API_TOKEN.
  -h, --help  this message.

Preconditions (checked loudly, in order):
  1. /usr/local/bin/certbot present
  2. dns-cloudflare plugin listed in \`certbot plugins\`
     (if absent, install: sudo apt-get install -y python3-certbot-dns-cloudflare)
  3. CLOUDFLARE_API_TOKEN set in $HERMES_ENV  (never committed to git)
  4. shared key at $KEY_FILE (created by this script if absent; on dots it
     lands via \`ctl settings intercom.cert_key\` — D-CERT card 3's job)

Staging: scp bundle.bin to $HA_TARGET:$HA_STAGE_DIR/bundle.bin (+ bundle.json
sidecar, untrusted-readable metadata for the daily heartbeat), then ssh
sha256sum readback must match or the script exits 1.
EOF
}

log() { printf '[dot-cert] %s\n' "$*" >&2; }
die() { log "FATAL: $*"; exit 1; }

command -v certbot >/dev/null || die "certbot not on PATH. Install: sudo apt-get install -y certbot"
command -v scp >/dev/null || die "scp not on PATH"
command -v ssh >/dev/null || die "ssh not on PATH"
[[ -f /usr/bin/python3 ]] || die "/usr/bin/python3 not found"
[[ -f "$AESGCM_HELPER" ]] || die "cipher helper missing: $AESGCM_HELPER"

# --- mode parsing (before preconditions so --help works tokenless) ----------
MODE="${1:-issue}"
DRY_FLAG=()
if [[ "${2:-}" == "--dry-run" || "$MODE" == "--dry-run" ]]; then DRY_FLAG=(--dry-run); fi
case "$MODE" in
  issue|--dry-run|--renew) ;;
  -h|--help|help) usage; exit 0 ;;
  *) usage; die "unknown mode: $MODE (use issue | --renew | --dry-run | --help)" ;;
esac

# --- precondition 2: dns-cloudflare plugin ----------------------------------
if ! certbot plugins 2>/dev/null | grep -q '^\* dns-cloudflare'; then
  log "dns-cloudflare plugin NOT installed. Exact remediation:"
  echo "  sudo apt-get install -y python3-certbot-dns-cloudflare"
  exit 1
fi

# --- precondition 3: Cloudflare token ---------------------------------------
# Do NOT commit the token. Script pulls it from the hermes env file.
TOKEN_PRESENT=0
if [[ -f "$HERMES_ENV" ]] && grep -q '^CLOUDFLARE_API_TOKEN=' "$HERMES_ENV"; then
  TOKEN_PRESENT=1
elif [[ -n "${CLOUDFLARE_API_TOKEN:-}" ]]; then
  TOKEN_PRESENT=1
fi
if [[ "$TOKEN_PRESENT" != 1 ]]; then
  echo "FATAL: CLOUDFLARE_API_TOKEN not found in $HERMES_ENV (and not exported)." >&2
  echo "Remediation: add the line  CLOUDFLARE_API_TOKEN=<zone-dns-edit token>  to $HERMES_ENV" >&2
  echo "(token needs Zone:DNS:Edit for jagbains.org. This is a hard precondition — not staging anything." >&2
  echo " Without it there is no issuance and no bundle, by design.)" >&2
  exit 1
fi
CLOUDFLARE_API_TOKEN="$(grep '^CLOUDFLARE_API_TOKEN=' "$HERMES_ENV" | head -1 | cut -d= -f2- || true)"
[[ -n "${CLOUDFLARE_API_TOKEN:-}" ]] || die "empty CLOUDFLARE_API_TOKEN in $HERMES_ENV"

# --- certify credentials file (never committed) -----------------------------
mkdir -p "$(dirname "$CRED_FILE")"
if [[ ! -f "$CRED_FILE" ]]; then
  # write real value, then lock down
  umask_was="$(umask)"; umask 077
  printf 'dns_cloudflare_api_token = %s\n' "$CLOUDFLARE_API_TOKEN" > "$CRED_FILE"
  umask "$umask_was"
  log "wrote certbot credentials to $CRED_FILE (chmod 600). Do NOT commit this file."
else
  # keep perms tight on an existing file
  chmod 600 "$CRED_FILE" 2>/dev/null || true
fi

# --- precondition 4 / shared key (D-CERT-1) ----------------------------------
KEY_FRESH=0
if [[ ! -f "$KEY_FILE" ]]; then
  mkdir -p "$(dirname "$KEY_FILE")"
  ( umask 077 && openssl rand -out "$KEY_FILE" 32 ) \
    || die "could not generate $KEY_FILE (openssl rand failed)"
  KEY_FRESH=1
  log "generated NEW 32-byte shared key at $KEY_FILE (chmod 600)."
  log "REMEMBER (card 3): each dot needs this key via:"
  log "    ctl settings intercom.cert_key=\"\$(cat $KEY_FILE | base64)\""
  log "    ^ that dot-side ctl call is D-CERT card 3's job — this card does NOT touch dots."
else
  chmod 600 "$KEY_FILE" 2>/dev/null || true
fi

# --- certbot run (mode parsed above) ------------------------------------------
# certbot's state (/etc/letsencrypt) is root-owned; when not already root,
# elevate ONLY the certbot invocation: password via the NOPASSWD cat rule
# (/etc/hermes-sudo/sudo-password), piped to sudo -S on stdin. Everything
# AFTER certbot (encrypt + staging + readback) runs UNPRIVILEGED — the ssh
# key for root@192.168.10.83 lives in jagb's ~/.ssh, and a root ssh would
# miss jagb's known_hosts ("Host key verification failed").
CERTBOT_BIN=(certbot)
if [[ "$(id -u)" != "0" ]]; then
  PW_FILE="/etc/hermes-sudo/sudo-password"
  sudo -n cat "$PW_FILE" >/dev/null 2>&1 || die "cannot read $PW_FILE; install/refresh the sudo NOPASSWD cat rule"
  CERTBOT_BIN=(sudo -S -p '' certbot)
  export DOTCERT_SUDO_PW="$(sudo -n cat "$PW_FILE")"
fi
run_certbot() { if [[ "$(id -u)" != "0" ]]; then printf '%s\n' "$DOTCERT_SUDO_PW" | "${CERTBOT_BIN[@]}" "$@"; else certbot "$@"; fi; }

case "$MODE" in
  --renew)
    log "renewal path: certbot renew --cert-name $CERT_NAME ${DRY_FLAG[*]}"
    run_certbot renew --cert-name "$CERT_NAME" --non-interactive "${DRY_FLAG[@]+"${DRY_FLAG[@]}"}"
    ;;
  *)
    log "issuance path: certbot certonly --dns-cloudflare -d $CERT_DOMAIN -d '*.$CERT_DOMAIN'"
    run_certbot certonly \
      --dns-cloudflare \
      --dns-cloudflare-credentials "$CRED_FILE" \
      --dns-cloudflare-propagation-seconds 30 \
      -d "$CERT_DOMAIN" -d "*.$CERT_DOMAIN" \
      --cert-name "$CERT_NAME" \
      --non-interactive --agree-tos "${DRY_FLAG[@]+"${DRY_FLAG[@]}"}"
    ;;
esac

# A --dry-run run validates the challenge end-to-end and produces NO live dir
# (certbot stays inside its staging dirs) — that IS success; exit 0 here.
if [[ ${#DRY_FLAG[@]} -gt 0 ]]; then
  log "dry-run complete: challenge path validated; nothing staged"
  exit 0
fi

# --- locate live cert dir -----------------------------------------------------
# certbot's /etc/letsencrypt is root-only. When unprivileged, copy the live
# pair into $WORK_DIR via the sudo pattern, then work unprivileged from here.
LIVE_DIR="/etc/letsencrypt/live/$CERT_NAME"
if [[ "$(id -u)" != "0" ]]; then
  printf '%s\n' "$DOTCERT_SUDO_PW" | sudo -S -p '' sh -c "cp -RL '$LIVE_DIR'/. '$WORK_DIR/' && chmod 600 '$WORK_DIR'/privkey.pem && chown -R $(id -u):$(id -g) '$WORK_DIR'" \
    || die "cannot read $LIVE_DIR via sudo"
  WORK_LIVE="$WORK_DIR"
else
  WORK_LIVE="$LIVE_DIR"
fi
[[ -s "$WORK_LIVE/fullchain.pem" && -s "$WORK_LIVE/privkey.pem" ]] || die "no certbot live dir at $LIVE_DIR after issuance"
NOT_AFTER="$(openssl x509 -enddate -noout -in "$WORK_LIVE/cert.pem" | cut -d= -f2)"
FP="$(openssl x509 -fingerprint -sha256 -noout -in "$WORK_LIVE/cert.pem" | cut -d= -f2)"
log "cert covers: $(openssl x509 -subject -noout -in "$WORK_LIVE/cert.pem") expires: $NOT_AFTER fp: $FP"

# --- encrypt (AES-256-GCM via helper) ----------------------------------------
BUNDLE="$WORK_DIR/bundle.bin"
/usr/bin/python3 "$AESGCM_HELPER" encrypt "$WORK_LIVE" "$KEY_FILE" "$BUNDLE"
LOCAL_SHA="$(sha256sum "$BUNDLE" | awk '{print $1}')"
log "local bundle sha256=$LOCAL_SHA"

# --- stage on HA (D-CERT-2 dumb bucket) --------------------------------------
ssh "$HA_TARGET" "mkdir -p '$HA_STAGE_DIR'"
scp -q "$BUNDLE" "$HA_TARGET:$HA_STAGE_DIR/bundle.bin" \
  || die "scp of bundle.bin to HA failed"
STAGED_AT="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
MODE_ECHO="$([[ ${DRY_FLAG:+set} ]] && echo dry-run || echo live)"
cat > "$WORK_DIR/bundle.json" <<EOF
{
  "cert_name": "$CERT_NAME",
  "domain": "$CERT_DOMAIN",
  "wildcard": "*.$CERT_DOMAIN",
  "not_after": "$NOT_AFTER",
  "fingerprint_sha256": "$FP",
  "bundle_sha256": "$LOCAL_SHA",
  "staged_at": "$STAGED_AT",
  "mode": "$MODE_ECHO",
  "key_derivation_hint": "AES-256-GCM, 12-byte nonce prepended per ECD1 envelope; shared key = sha256 of the 32-byte file at ~/.hermes/keys/dot-cert-key (dots hold it via ctl settings intercom.cert_key, D-CERT card 3)"
}
EOF
scp -q "$WORK_DIR/bundle.json" "$HA_TARGET:$HA_STAGE_DIR/bundle.json" \
  || die "scp of bundle.json sidecar to HA failed"

# --- verify staged readback (scp is not success until sha matches) -----------
REMOTE_SHA="$(ssh "$HA_TARGET" "sha256sum '$HA_STAGE_DIR/bundle.bin'" | awk '{print $1}')"
if [[ "$LOCAL_SHA" != "$REMOTE_SHA" ]]; then
  die "staged readback sha256 mismatch: local=$LOCAL_SHA remote=$REMOTE_SHA"
fi
log "staged readback verified (sha256 match: $REMOTE_SHA)"
log "bundle staged at $HA_TARGET:$HA_STAGE_DIR/bundle.bin (sidecar bundle.json alongside)"

# --- ALSO redeploy the live pair to every host that serves it (runbook-wide):
#   1. HA core TLS (:8123) /ssl/ + core restart
#   2. NPM custom_ssl npm-1/ + addon restart
#   3. Proxmox /etc/pve/local/pveproxy-ssl.{pem,key} + pveproxy restart
# Dots are NOT covered here (certfetch self-updates — D-CERT-4).
log "deploying the live pair to HA /ssl, NPM custom_ssl, and the PVE host"
scp -q -o BatchMode=yes "$WORK_LIVE/fullchain.pem" "$WORK_LIVE/privkey.pem" \
  "$HA_TARGET:/ssl/" || log "WARN: /ssl scp failed (HA core keeps old cert)"
scp -q -o BatchMode=yes "$WORK_LIVE/fullchain.pem" "$WORK_LIVE/privkey.pem" \
  "$HA_TARGET:/app_configs/a0d7b954_nginxproxymanager/custom_ssl/npm-1/" \
  || log "WARN: NPM custom_ssl scp failed"
scp -q -o BatchMode=yes "$WORK_LIVE/fullchain.pem" root@192.168.10.13:/tmp/pve-fullchain.pem \
  && scp -q -o BatchMode=yes "$WORK_LIVE/privkey.pem" root@192.168.10.13:/tmp/pve-privkey.pem \
  && ssh -o BatchMode=yes root@192.168.10.13 \
    'cp /tmp/pve-fullchain.pem /etc/pve/local/pveproxy-ssl.pem \
      && cp /tmp/pve-privkey.pem /etc/pve/local/pveproxy-ssl.key \
      && chmod 640 /etc/pve/local/pveproxy-ssl.* \
      && systemctl restart pveproxy' \
  || log "WARN: PVE pveproxy-ssl deploy failed"
ssh -o BatchMode=yes "$HA_TARGET" "ha apps restart a0d7b954_nginxproxymanager" >/dev/null 2>&1 \
  || log "WARN: NPM addon restart via ha CLI failed"
ssh -o BatchMode=yes "$HA_TARGET" "ha core restart" >/dev/null 2>&1 \
  || log "WARN: HA core restart failed"
log "live pair deployed: HA /ssl, NPM custom_ssl (npm-1), PVE pveproxy-ssl; dots self-update via certfetch"

rm -rf "$WORK_DIR"