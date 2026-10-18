# Decision D-ANN: dot announcements (live-record capture + replay to echolocal dots)

Grilled with Jag 18 Oct 2026. Status: DECIDED, not yet implemented.
Doc form matches dot-p2p-decisions.md. Roadmap item: announcements (next
after D-P2P day-2). Companion decision class to D-DI/D-P2P; signalling and
orchestration stay HA-owned, per the same split as D-P2P-2.

## Scope / target set

- **D-ANN-1: echolocal dots only.** Target class = registered echolocal dots
  (kitchen, lounge today). Other HA media players / speaker groups are out of
  scope for v1; if ever added, the target list becomes a service argument
  rather than baked-in logic.
- **D-ANN-2: Alexa semantics for invocation.** The invoking dot is EXCLUDED
  from the replay set (you're standing at it). Two addressing modes:
  bare "announce" = all other echolocal dots; "announce to <room>" = that one
  dot. Room resolution reuses the drop-in directory logic.

## Invocation and capture (the PRIMARY mode is live-record replay)

- **D-ANN-3: capture is the primary use case.** Wake-word "announce" → chime →
  fixed capture window (**15s**) with **early bail on silence**. TTS-only
  announcements are NOT built unless capture scaffolding makes it free.
- **D-ANN-4: invocation = wake-word sentence "okay nabu, announce [to
  <room>]".** No button trigger, no two-stage turn. The assist turn must END
  cleanly before capture opens (mic ownership moves to the announce/capture
  lane afterwards) — the 16 Oct frozen-event-loop incident (t_650b2f99) is the
  hazard class to respect.
- **D-ANN-5: STT trigger list must be live-verified.** Test the trigger with
  the REAL heard text from `heard slot=... text=` in logcat, not hand-typed
  `/api/conversation/process` — "announce" transcribes unpredictably and
  hand-typed tests do not catch it (established drop-in gotcha).
- **D-ANN-6: no end-of-capture signal on the invoking dot.** Chime at capture
  start only; the announcement playing elsewhere is the natural "it happened"
  signal.

## Transport and playback

- **D-ANN-7: origin-dot serving (pull).** Invoking dot stores the capture and
  serves it over its existing HTTPS web surface (the TLS/D-CERT path).
  HA orchestrates: targets the receivers per D-ANN-2 and each receiver
  fetches + plays. No ICE/firewall/media-chain work — an announcement is
  finite (~15s) and not latency-sensitive, so pull beats push. HA relay stays
  mothballed per D-P2P.
- **D-ANN-8: dot-to-dot fetch with no pairing ceremony.** Matches the plain
  dot-to-dot precedent; the phone pairing gate is NOT applied to dot announce
  fetches.
- **D-ANN-9: receiver playback preceded by a chime** on the receiver dot
  (stock-Alexa behaviour). Receivers stay SILENT during the invoking dot's
  capture window — no ready-tone; nothing until playback.
- **D-ANN-10: receiver dot suspends its assistant pipeline during announce
  playback** — otherwise its mic ingests the announcement (`heard/replying`
  hazard class, the 15 Oct P2P finding).
- **D-ANN-11: announce playback LED = purple** (distinct from drop-in green
  ring/in-call, t_05fc05ad). New LED effect; green stays for the call lane.

## Failure and contention

- **D-ANN-12: offline target → retry ~10s, then skip (logged).** Finite
  catch-up announcements make retry cheap.
- **D-ANN-13: contention with the call lane → announce yields.** If a target
  dot is IN-CALL: skip that dot (log). If merely RINGING: delay the announce
  on that dot until the call state resolves. Never collide with call audio.
- **D-ANN-14: capture window hard-cuts at 15s** if the speaker is
  mid-sentence. No grace.

## Carding notes

- Cards split per the established one-verification-cycle-per-card rule:
  likely (1) dot-side capture + serve + playback machinery (Go, echod),
  (2) HA orchestration service + automations/intents, (3) deploy + live E2E
  with pre-staged log capture. Live-STT verification of the trigger phrases
  belongs in the E2E card.
- Any new intercom config field must land WITH its PairingTable ctl row in
  the same card (registration-gap gotcha).