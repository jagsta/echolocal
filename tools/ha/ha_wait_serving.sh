#!/usr/bin/env bash
# ha_wait_serving.sh — post-restart HA core serving probe (echolocal anomaly 3).
#
# The HAOS HTTP wedge: :8123 accepts TCP then closes every HTTP request, and
# `ha core start` exits 0 without fixing it. TCP-accept from OUTSIDE does not
# mean serving — the wedge was only visible from INSIDE the box. So this script
# sshes to the HA host and probes :8123/api/ LOCALLY there (127.0.0.1).
#
# NOTE on schemes: if the HA config has ssl: true (this box does), 8123 is TLS
# and plain http:// gets an empty reply (code 000, curl exit 52) even when
# perfectly serving. So each probe tries BOTH https:// and http://; serving =
# 200 or 401 on either (401 = HTTP alive and auth-gated).
#
# Usage: ha_wait_serving.sh <ssh-target> [timeout-seconds]
#   <ssh-target>       HA host, e.g. root@192.168.10.83 (or an ~/.ssh/config
#                      alias). Nonstandard ssh port: HA_SSH_PORT=22222 env or
#                      arg3.
#   [timeout-seconds]  wait budget before the one automatic full restart
#                      (default 120)
#
# On timeout: log FAIL, run `ha core restart` once, re-probe; only a second
# timeout exits nonzero. Exit 0 = serving.
set -uo pipefail

TARGET="${1:?usage: ha_wait_serving.sh <ssh-target> [timeout-seconds] [ssh-port]}"
TIMEOUT="${2:-120}"
SSH_PORT="${HA_SSH_PORT:-${3:-}}"

log() { printf '[ha-wait-serving] %s\n' "$*" >&2; }

SSH_OPTS=(-o BatchMode=yes -o ConnectTimeout=10)
[[ -n "$SSH_PORT" ]] && SSH_OPTS+=(-p "$SSH_PORT")

# One probe round over ssh, run on the HA box itself. Two in-box realities to
# handle (both observed live on this stack, 2026-10-15):
#   - 127.0.0.1:8123 can be CONNECTION-REFUSED on HAOS even when HA is fine —
#     core listens on the docker bridge (172.30.32.1, resolvable as
#     "homeassistant"), not on host loopback.
#   - with ssl: true in HA's config, 8123 is TLS; plain http:// gets an empty
#     reply (code 000, curl exit 52) even when perfectly serving.
# So probe BOTH addresses and BOTH schemes. Serving = 200 or 401 on any
# combination (401 = HTTP alive and auth-gated). This stays "from inside the
# box" — the wedge in anomaly 3 passed TCP from outside but closed requests,
# which this probe would have caught via the 000/empty-reply path.
REMOTE_PROBE='
P() { curl -sk --noproxy "*" --max-time 5 "$1/api/" -o /dev/null -w "%{http_code}"; }
for H in 127.0.0.1 homeassistant; do
  for S in https http; do
    C="$(P "$S://$H:8123")"
    case "$C" in 200|401) echo "$C"; exit 0 ;; esac
  done
done
echo "$C"
'
probe_once() {
  ssh "${SSH_OPTS[@]}" "$TARGET" "$REMOTE_PROBE" 2>/dev/null
}

serving_code() {
  local code
  code="$(probe_once)"
  [[ "$code" =~ ^(200|401)$ ]] || return 1
  printf '%s' "$code"
}

wait_loop() {
  local budget="$1" waited=0 code=""
  while (( waited < budget )); do
    if code="$(serving_code)"; then
      log "HA serving (127.0.0.1:8123/api/ -> HTTP $code) after ${waited}s"
      return 0
    fi
    log "not serving yet (code='${code:-none}') waited=${waited}s/${budget}s"
    sleep 5
    waited=$(( waited + 5 ))
  done
  log "FAIL: not serving after ${budget}s (last code='${code:-none}')"
  return 1
}

# --- phase 1: wait up to TIMEOUT for core to come back serving ---------------
if wait_loop "$TIMEOUT"; then
  exit 0
fi

# --- phase 2: one automatic full restart, then re-probe ----------------------
log "running one automatic 'ha core restart' and re-probing"
ssh "${SSH_OPTS[@]}" "$TARGET" "ha core restart" >/dev/null 2>&1 \
  || log "WARN: ha core restart command failed (still re-probing)"

sleep 10
if wait_loop "$TIMEOUT"; then
  log "recovered after automatic full restart"
  exit 0
fi

log "FAIL: HA core still not serving after restart + ${TIMEOUT}s re-probe"
exit 1