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

### D-DI-6a — offline-at-dial contract (t_3c196f3e, settled 15 Oct 2026)
Live defect: the dial automation fed the aspirational slug `echod-<room>` into `echolocal_dropin.call`, so a room with no dot (spoken "lounge" → area `big_lounge`) registered a RINGING session against a PHANTOM callee; the caller sat DIALING until the 30s ring timeout and every retry was busy-rejected.
**Contract (of the three candidate fixes, option 1 is THE contract):** the offline-at-dial path is the dial contract — registered as service `echolocal_dropin.dial` (→ `runtime.call_or_report_offline`), with the callee resolved UP FRONT via `area_aliases.json` (option 2's alias resolution folded in): explicit dot device → alias/area id → `AREA_TO_DEVICE`; refusal (`CallError` "no drop-in dot in …") BEFORE any session exists for a dotless/unknown room; an OFFLINE known dot gets TTS "The <room> isn't available" at the caller with no session. `echolocal_dropin.call` stays for explicit device-to-device calls; option 3 (gc_tick reaping phantom callees) is superseded — the phantom endpoint can no longer exist.
Verification: unit tests pin it (`tests/test_offline_dial.py`); the deployed automation `echolocal_dropin_dial` calls `echolocal_dropin.dial` with `callee_room` only (no `callee_device` slug).

## D-DI-7 — Verification posture
Every card carries a criteria block (grep-able CHECK or pytest on the Hermes venv python absolute path per hil-framework convention); protocol-level cards (does go-esphome-device support continuous sideband audio?) must be resolved by READING the pilot device's echod/go-esphome-device before session-layer cards are authored — D-DI-4's relay shape depends on the answer.

## Upstream posture (Jag, 11 Oct)
Repo layout: fork `main` = clean upstream mirror (upstream remote = ygelfand/echolocal); ALL drop-in work lives on branch `feat/dropin-intercom`. Upstreaming is OPTIONAL but kept straightforward — if it happens, the PR split is: (a) `internal/feature/intercom` + config/wiring seams → PR to **ygelfand/echolocal**; (b) the HA `echolocal_dropin` component's device/dashboard pieces → PR to **ygelfand/echolocal-hacs** (his companion repo); (c) Jag-area specifics (area_aliases.json etc.) stay fork-local. Specs/findings docs: if upstreaming, offer them as PR docs; otherwise they remain fork-local on the branch. Until then: rebase `feat/dropin-intercom` onto upstream/main periodically to keep the PR surface small.

## Roadmap (post-v1, not authored as cards yet)
- **3-way drop-ins** (Jag, 11 Oct): a third device joins an existing call — multi-party star relay in HA (one session, N endpoints, not pairwise bridge), busy-reject semantics extended (3rd-device invite to an active call = "join offer" to participants rather than plain busy), join/leave chime + LED states. CONSTRAINT to honor in cards 2/3/5 NOW: the session state machine and relay topology must not hard-code "exactly two endpoints" — keep the endpoint list a list (add-participant operation, one-relay-to-N fan-out), so 3-way becomes additive, not a refactor. The probe's "one perpetual turn per device" limitation still bounds each device to one audio stream regardless of party count.
- Direct P2P data path swap (D-DI-4 v2) + open-duplex gated on pilot ERLE measurements.
- v2 protocol extension in go-esphome-device (only needed once P2P lands).

## Open items (Jag, non-blocking but tracked)
- Confirm how many Gen 2 Dots are available; buy 2nd unit for end-to-end pair testing. HARDWARE CONTEXT (11 Oct): the existing live pilot is a biscuit on FireOS 6 (not 5.1) — echod runs on it, so assume FireOS 6 as the reference stack for debugging. PROCUREMENT RULE for unit #2: check FireOS version BEFORE letting the device online; unlock guides are firmware-version-sensitive — unlock over USB before first boot if possible.
- Gen 3 (D9N29T) owned but NOT on the drop-in critical path: an echod port to Gen 3 (different mic/loopback ALSA layout, unverified DAC-loopback AEC reference, FireOS 6 delta) is a separate future project, not a v1 enabler; the D9N29T unlock route is JUL14N's hassmic project, a different satellite stack. Do NOT unlock the Gen 3 as part of this project.
- Upstream etiquette: feature proposal to ygelfand (Discord linked in repo) once the fork has a working prototype — decide then whether we PR upstream or keep fork-local.