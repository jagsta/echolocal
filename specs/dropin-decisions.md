# Drop-In Intercom — Grilled Decisions (D-DI-1..7)

Grilled with Jag 11 Oct 2026. These are the settled decisions; implementation sessions implement from here, do not re-derive. Supersedes nothing; companion research lives at jagsta/research `ha-voice-echo-replacement.md`.

Scope: two-way drop-in intercom between EchoLocal satellites (Echo Dot Gen 2 running echod), built on **jagsta/echolocal** (this fork of ygelfand/echolocal). Goal is Echo drop-in parity for the household, phased.

## D-DI-1 — Implementation base: EchoLocal
Build on EchoLocal (`echod` Go daemon + go-esphome-device), NOT raw ESPHome satellites and NOT a hybrid bridge. Reuses Echo's far-field mic array and hardware AEC; userspace Go daemon is the right place for session logic. A live pilot Dot already runs EchoLocal in Jag's HA (more units available — Jag to confirm hardware before protocol work starts).

## D-DI-2 — Duplex model: phased C with goal A
- **v1: half-duplex** with VAD-driven squelch — both ends nominally open; receiving side mutes its transmit while the far end is talking.
- **v2 (stated goal): open duplex** gated on verifying whether the Gen 2's hardware AEC path is reachable through echod's audio stack.
- Ring/answer/mute/end machinery is identical for both phases; duplex mode is a switch behind the session layer.

## D-DI-3 — Permissions & ring contract
- Permission model: **per-pair map** (source→target), schema from day one; unset pairs **default to allow-household** so rollout behaves trust-wide without config.
- **Ring-first in v1, all cases**: callee gets chime + LED + "incoming from kitchen"; accepts via voice "answer" or button. `silent_connect` reserved as a per-pair schema field for later; **announce/listen-in-only mode is deferred** (not in v1).
- **3-second minimum audible ring** on the callee before "answer" can connect (hard rule, anti-ambush).

## D-DI-4 — Data path: HA-signalled, HA-relayed (v1), direct P2P reserved (v2)
v1: both Dots use their existing ESPHome native API connections; a HA-side component relays PCM and owns call state, permissions, ducking orchestration. Consequence accepted: **v1 calls drop if HA restarts** (one auto-reconnect attempt on comeback, callee end-chime, no retry storm). v2: data path swaps to direct echod↔echod LAN streaming behind the SAME session services; protocol extension lands in go-esphome-device at that point.

## D-DI-5 — Conversational UX contract
- Caller: assist intent phrases "**drop in on [room]**" / "**intercom [room]**" — wake word + full phrase in one breath (no follow-up-mode invocation; prevents ambient-conversation false calls).
- Callee accepts: "**answer**" (after the 3s ring floor) or button. Callee ends: "**hang up**" or "**stop**" (stop-as-hangup matches Echo's pattern, accepted ambiguity) or button.
- Wake word **suppressed on both ends during active calls**.
- **Max-duration kill-switch: 30 minutes** (configurable) — garbage-collector against forgotten open lines.
- Callee media: **pause, not duck**, resume after call (both ends).
- Chimes/LED: distinct ring chime + pulsing LED; connect chime + solid LED during call; end chime on teardown; map onto echod's existing LED conventions.

## D-DI-6 — Failure modes (cards vs accepted risks)
CARDS (explicit work):
- Call-drop handling on HA disconnect: single reconnect attempt, callee end-chime (never silence).
- Busy-reject: a device mid-call rejects new incoming calls with a busy chime — no queueing, no call-waiting. Simultaneous mutual dial resolved deterministically (caller with lower device-id wins; loser told "busy").
- Mic mute via physical button during calls (amber LED), unmute = button again.
- 3-second ring floor (spec decision, enforced in session state machine).
ACCEPTED RISKS / trivial paths (no card): offline-at-dial → immediate TTS "kitchen isn't available" on caller, no retry; accidental activation → bounded by one-breath invocation + kill-switch; no PIN gate in v1.

## D-DI-7 — Verification posture
Every card carries a criteria block (grep-able CHECK or pytest on the Hermes venv python absolute path per hil-framework convention); protocol-level cards (does go-esphome-device support continuous sideband audio?) must be resolved by READING the pilot device's echod/go-esphome-device before session-layer cards are authored — D-DI-4's relay shape depends on the answer.

## Open items (Jag, non-blocking but tracked)
- Confirm how many Gen 2 Dots are available; buy 2nd unit for end-to-end pair testing.
- Upstream etiquette: feature proposal to ygelfand (Discord linked in repo) once the fork has a working prototype — decide then whether we PR upstream or keep fork-local.