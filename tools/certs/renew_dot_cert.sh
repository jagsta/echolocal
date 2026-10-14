#!/usr/bin/env bash
# D-CERT card 2 — cron wrapper: daily renewal + re-encrypt + re-stage.
# Stdlib only (bash + openssl), logs to stdout, exit code propagates failure.
set -uo pipefail
SCRIPT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
LOG_PREFIX="[dot_cert_renew $(date -u +%Y-%m-%dT%H:%M:%SZ)]"
echo "$LOG_PREFIX starting issue_and_stage.sh --renew"
"$SCRIPT/issue_and_stage.sh" --renew
rc=$?
if [[ $rc -eq 0 ]]; then
  echo "$LOG_PREFIX OK"
else
  echo "$LOG_PREFIX FAILED rc=$rc (see above; commonly: token missing, plugin missing, scp/readback mismatch)"
fi
exit "$rc"