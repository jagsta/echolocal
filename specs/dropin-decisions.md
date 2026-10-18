# Drop-In Intercom — Grilled Decisions (D-DI-1..7)

Grilled with Jag 11 Oct 2026. These are the settled decisions; implementation sessions implement from here, do not re-derive. Supersedes nothing; companion research lives at jagsta/research `ha-voice-echo-replacement.md`.

Scope: two-way drop-in intercom between EchoLocal satellites (Echo Dot Gen 2 running echod), built on **jagsta/echolocal** (this fork of ygelfand/echolocal). Goal is Echo drop-in parity for the household, phased.

## D-DI-1 — Implementation base: EchoLocal
Build on EchoLocal (`echod` Go daemon + go-esphome-device), NOT raw ESPHome satellites and NOT a hybrid bridge. Reuses Echo's far-field mic array and hardware AEC; userspace Go daemon is the right place for session logic. A live pilot Dot already runs EchoLocal in Jag's HA (more units available — Jag to confirm hardware before protocol work starts).

## D-DI-2 — Duplex model: phased C with goal A
- **v1: half-duplex** with VAD-driven squelch — both ends nominally open; receiving side mutes its transmit while the far end is talking.
- **v2 (stated goal): open duplex** gated on verifying whether the Gen 2's hardware AEC path is reachable through echod's audio stack.
- Ring/answer/mute/end machinery is identical for both phases; duplex mode is a switch behind the session layer.

### D-DI-2a — witnessed open-duplex gate result (18 Oct 2026, t_b8ae0a2f): **GO**
Witnessed run lounge→kitchen, both dots on 34f8168 (md5 686a43e6), cancellers ON then OFF,
3-channel takes (center mic / DAC reference / listener out) captured on the kitchen dot.

- Echo return loss (ref→out): **29.7 dB** cancel ON vs **2.5 dB** cancel OFF — ~27 dB bought by
  the canceller. MDF self-reported ERLE 20.7–21.2 dB (running/learning true). Echo-path lag
  **114 samples = 7.1 ms**, well inside the 1024-tap / 64 ms window, so tail coverage is not the
  binding constraint.
- Double-talk, Jag's ear: cancel ON — *"minor echo, no chop"*; cancel OFF — *"severe echo,
  causing feedback looping"* (control run valid, as required).
- Instrumented caveat: in frames where both ends are active the near-end deliverable sits
  14.6 dB below the mic (out = mic − 14.6) with cancel ON, vs 14.1 dB *above* it with cancel OFF
  — ~29 dB of active echo suppression is still engaging (`aec.EchoSuppressActive = -40`,
  libcountertop canceller.go:14). It was **not** audible as chop in this run.

Decision: v2 open duplex **rides the existing canceller**; duplex mode remains a switch behind
the session layer (machinery identical). Tuning scope to weigh while flipping it: EchoSuppressActive
−40 vs the library default −15 (echolocal's `mic.newSuppressor` calls `aec.NewSuppressor`, which
hardcodes −40 — tuning needs a direct `aec.Preprocessor` construction or an upstream change);
1024 taps against a measured 7.1 ms echo path; AGC freeze while sounding. `Source.Dropped()` still
has no CLI caller — expose it in `ctl audio cancel` before judging armv7 headroom under sustained
duplex.

### D-DI-2b — tuning-scope measurements (18 Oct 2026, t_4abd9621)
All numbers from offline replays of the witnessed take (`take-phase1.wav`, cancel ON) through the
real pipeline (`internal/hardware/mic` TestReplayPipeline / TestReplaySuppressSweep /
TestCancellerCPUSweep on the pinned libcountertop). DT = frames with both ends active (n=34).

**1. EchoSuppressActive (residual-suppressor dip while echo is active).** Sweep on identical
replay input:

| EchoSuppressActive | ERLE ref→out | DT mic→out | DT out dBFS |
|---|---|---|---|
| −15 (library default) | 29.7 dB | +13.6 | −50.0 |
| −25 | 30.1 | +14.2 | −50.5 |
| −30 | 30.2 | +14.3 | −50.7 |
| −40 (shipped, hardcoded) | 30.4 | +14.5 | −50.8 |
| suppressor off | 16.2 | −1.0 | −35.4 |

The dip is **almost free**: −15 vs −40 buys 0.7 dB of near-end double-talk deliverability on
measurement. It is a severity floor the suppressor only reaches when the MDF's own residual is
loud; the linear canceller does the work. Any move from −40 must be judged by ear (musical-noise/
residual-tail risk rises as the dip loosens — that cannot be measured headless). **Recommendation:
leave −40; revisit only if a live ear run reports chop in v2.**

**2. Taps (1024 = 64 ms window vs measured 7.1 ms path).** ERLE with denoise, warm filter:
256 taps 31.5 dB / 512 26.1 / 1024 (t1024-cold 30.4, t4096 31.0) — replay is single-echo-path so
the tail-depth difference does not bind; CPU per 16 kHz sample (amd64, relative only):
256 taps 1.00 µs, 512 0.73, 1024 1.15 — 17–35x realtime headroom even at 1024, and armv7 scale
tracks the FFT block count linearly enough that 1024 stays comfortably inside one core's budget
(parent card measured Idle 95–97%). The house has TWO dots (lounge→kitchen) — the room path is
short and the 57 ms of spare window costs little. **Recommendation: keep 1024; fewer taps buy
nothing measurable and would cost re-verifying every deploy.**

**3. AGC freeze while sounding.** Already implemented, no change needed: the leveler is updated
per-frame with `s.SetPlayback(sounding)` (mic.go broadcast), and libcountertop's leveler holds the
floor and skips gain movement when `sounding` is set (level.go observe/apply). This is why
feedback looping appears OFF but not ON.

**Knob that matters (not tunable in echolocal today): near-end double-talk deliverability under
denoise.** The suppressor applies ~14–15 dB of gain reduction to near-end speech whenever the MDF
reports echo residual, i.e. essentially always in open duplex (gain floor =
exp(0.2302585·EchoSuppressActive) weighted by pframe ≈ 1 while sounding). Raising
EchoSuppressActive to 0 dB is one line on the Preprocessor (`p.EchoSuppressActive = 0` constructed
directly — no upstream change needed) and would hand near-end speech through untouched while the
MDF handles the echo, at the risk of hearing more residual tail. Numbers above say −15 ≈ −40
measurement-wise, so the choice between them is an ear judgement; −0 is NOT recommended
(residual echo would be unmasked, the OFF-take failure mode is evidence enough).

**Tooling shipped:** `TestReplaySuppressSweep` (SUPPRESS_REPLAY / SUPPRESS_REPLAY_OUT),
`TestCancellerCPUSweep` (BENCH_REPLAY), replay-variants in TestReplayPipeline, and
`~/.hermes/cache/scratch/bin/replay_erle.py`. `Source.Dropped()` now prints in
`ctl audio cancel` (bb91926) — the headroom seam for the sustained-duplex deploy card.

Decision: **no v1/v2 default changes without a witnessed ear run.** If a live v2 open-duplex run
reports chop, the candidate is EchoSuppressActive −40 → −15 (one line in `newSuppressor`
constructing the Preprocessor directly), NOT taps and NOT AGC.

## D-DI-3 — Permissions & ring contract
- Permission model: **per-pair map** (source→target), schema from day one; unset pairs **default to allow-household** so rollout behaves trust-wide without config.
- **Ring-first in v1, all cases**: callee gets chime + LED + "incoming from kitchen"; accepts via voice "answer" or button. `silent_connect` reserved as a per-pair schema field for later; **announce/listen-in-only mode is deferred** (not in v1).
- **1-second minimum audible ring** on the callee before "answer" can connect (hard rule, anti-ambush; amended from 3s — Jag, 16 Oct, t_903017d5: the announce TTS now precedes the ring (speak-before-ring), so the floor no longer needs headroom).

## D-DI-4 — Data path: HA-signalled, HA-relayed (v1), direct P2P reserved (v2)
v1: both Dots use their existing ESPHome native API connections; a HA-side component relays PCM and owns call state, permissions, ducking orchestration. Consequence accepted: **v1 calls drop if HA restarts** (one auto-reconnect attempt on comeback, callee end-chime, no retry storm). v2: data path swaps to direct echod↔echod LAN streaming behind the SAME session services; protocol extension lands in go-esphome-device at that point.

## D-DI-5 — Conversational UX contract
- Caller: assist intent phrases "**drop in on [room]**" / "**intercom [room]**" — wake word + full phrase in one breath (no follow-up-mode invocation; prevents ambient-conversation false calls).
- Callee accepts: "**answer**" (after the 1s ring floor, per the D-DI-3 amendment) or button. Callee ends: "**hang up**" or "**stop**" (stop-as-hangup matches Echo's pattern, accepted ambiguity) or button.
- Wake word **suppressed on both ends during active calls**.
- **Max-duration kill-switch: 30 minutes** (configurable) — garbage-collector against forgotten open lines.
- Callee media: **pause, not duck**, resume after call (both ends).
- Chimes/LED: distinct ring chime + pulsing LED; connect chime + solid LED during call; end chime on teardown; map onto echod's existing LED conventions.

## D-DI-6 — Failure modes (cards vs accepted risks)
CARDS (explicit work):
- Call-drop handling on HA disconnect: single reconnect attempt, callee end-chime (never silence).
- Busy-reject: a device mid-call rejects new incoming calls with a busy chime — no queueing, no call-waiting. Simultaneous mutual dial resolved deterministically (caller with lower device-id wins; loser told "busy").
- Mic mute via physical button during calls (amber LED), unmute = button again. **Amended (Jag, 15 Oct, t_4790db90): mute lives on the dot's DEDICATED mute button; the Action button ends the call ("tap to end") for both roles; volume buttons adjust volume (pass through to the media player, never consumed in-call).** Implemented in `internal/feature/intercom/wiring.go` `take()`; pinned by `TestTakeButtonContract`.
- 1-second ring floor (spec decision, amended to 1s per D-DI-3 — speak-before-ring removed the headroom need; enforced in session state machine, HA const + dot-side RingFloor).
ACCEPTED RISKS / trivial paths (no card): offline-at-dial → immediate TTS "kitchen isn't available" on caller, no retry; accidental activation → bounded by one-breath invocation + kill-switch; no PIN gate in v1.

### D-DI-6a — offline-at-dial contract (t_3c196f3e, settled 15 Oct 2026)
Live defect: the dial automation fed the aspirational slug `echod-<room>` into `echolocal_dropin.call`, so a room with no dot (spoken "lounge" → area `big_lounge`) registered a RINGING session against a PHANTOM callee; the caller sat DIALING until the 30s ring timeout and every retry was busy-rejected.
**Contract (of the three candidate fixes, option 1 is THE contract):** the offline-at-dial path is the dial contract — registered as service `echolocal_dropin.dial` (→ `runtime.call_or_report_offline`), with the callee resolved UP FRONT via `area_aliases.json` (option 2's alias resolution folded in): explicit dot device → alias/area id → `AREA_TO_DEVICE`; refusal (`CallError` "no drop-in dot in …") BEFORE any session exists for a dotless/unknown room; an OFFLINE known dot gets TTS "The <room> isn't available" at the caller with no session. `echolocal_dropin.call` stays for explicit device-to-device calls; option 3 (gc_tick reaping phantom callees) is superseded — the phantom endpoint can no longer exist.
Verification: unit tests pin it (`tests/test_offline_dial.py`); the deployed automation `echolocal_dropin_dial` calls `echolocal_dropin.dial` with `callee_room` only (no `callee_device` slug).

### D-DI-6b — ring-announce cue (t_61af01ce, settled 16 Oct 2026)
At ring, the CALLEE's dot speaks "<caller> is dropping in" before/alongside the ring chime.
**Mechanism (investigated; safe, no dot-side change):** `tts.speak` on the callee's `assist_satellite` entity — the exact route the offline-at-dial notice already uses. HA assist_satellite → `VoiceAssistantAnnounceRequest`; dot-side (go-esphome-device voice.go → libcountertop `turn/announce.go`) the announcement plays through the announce sound claim and NOTHING else: no voice turn is started (tts.speak never sets `StartConversation`, so no mic is consumed — the D-P2P-8 bug class cannot recur), the chime/LED wiring is untouched, and no P2P offer exists at ring (only constructed at connect). Rejected: esphome TTS/notify media on the satellite (no such channel on this stack), echod ctl (no announce subcommand; requires a dot build + deploy, out of scope for an HA-owned ring).
**Gate:** one-way entry option `announce_on_ring` (default ON). The cue is fire-and-forget: an announce failure logs and never tears the ring down (only dropin_dial transport failures do).
**Name:** ring events always carry `caller_name` — phone peers announce their display name (D-DI-8c), dot callers their room label ("little lounge is dropping in"). Mutual-dial reown rings carry it too.
Verification: `tests/test_ring_announce.py` (+ ring-event pin in `test_phone_peer.py`). Live deploy + E2E is a follow-up card; this card ships code + tests only.

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