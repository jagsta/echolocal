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
## Addenda — 19 Oct live-witness amendments (E2E session)

- **D-ANN-3a: bail counters are CONTIGUOUS, not cumulative.** The AEC sounds
  the chime + "Speak now." tail as digital silence after the lead-in
  (measured: literal zero frames); a cumulative counter let those frames
  fire both bails — dead-silence killed captures ~1.5s after the prompt
  (every human test 'never spoke' 4.5s) and trailing-silence cut the first
  breath pause mid-sentence. Dead-air bail = **6s contiguous** (heard==0),
  trailing bail = **3s contiguous** (Jag ruling 19 Oct: 1.5s was too
  aggressive and cut the message), reset on any post-lead-in speech frame.
- **D-ANN-3b: a capture with no meaningful speech ABORTS the lane** (never
  promoted to annCaptured). Publishing a silence-only slot wedged the lane
  for minutes: nothing fetches it, every later announce refused 'lane busy',
  receivers got hopeless 410s. Abort → annIdle → receivers get a clean 404.
- **D-ANN-3c: Capture SUPERSEDES a completed annCaptured slot.** One slot,
  overwritten by the next capture — the old serve-lazy-TTL-only clearing
  meant a second announce inside the 2-min TTL was refused AND the
  orchestration still re-played the PREVIOUS capture. In-flight states
  (annCapturing, annPlaying) still refuse.
- **D-ANN-11a: purple + the D-ANN-10 suspension span PLAYBACK, not just the
  play call.** The playback sink (speaker PlayStream) is fire-and-forget, so
  runPlay must hold for capture-duration + 500ms tail before releasing LED,
  suspension and lane — a bare return released all three at playback START
  (witnessed: purple extinguished during playback).
- **D-ANN-10a: LED/suspend/state release order on the receiver is explicit
  and idempotent** — early-return paths (fetch fail) release immediately and
  the deferred releases re-fire as no-ops after the hold.
- **Diagnostic recipe that cracked the mic-silence question** (record it
  once, it cost probes 1-7): `ctl audio record echo <secs> <path>` records
  three channels — center mic, loopback ref, what LISTENERS got — pull the
  WAV and diff per-frame RMS; `ctl audio cancel` gives canceller running/
  learning/ERLE. A silent LEVELED stream with a healthy mic peak = the AEC
  sounding path doing its job, not a broken mic — feed that conclusion into
  the counter design, never into a mic-hardware debug loop.

## 19 Oct (session 2) addenda — threshold recalibration + witness

- **D-ANN-15: the capture silence threshold is CALIBRATED TO ROOM-DISTANCE
  SPEECH, not close speech** — announceSilenceRMS 200 (−44 dBFS) counted
  nearly ALL of a real at-distance announce as silence (live: heard=45 <
  min 50 → publish abort → receiver 404, no playback; capture deadlocked to
  the 15s hard cut because heard<min also blocks the trailing bail). New
  value 64 (−54 dBFS): catches ~50% of speech frames on the leveled feed,
  ambient floor (median ~16) stays under it. Committed 2d7cf79, witnessed
  PASS live (capture 8.24s via trailing-silence bail → receiver played).
  Known cost: in a noisy room `quiet` may never reach the bails → the
  capture runs the full 15s window — acceptable; missing the announce is not.
- **D-ANN-16 (OPEN, quirk witnessed): the capture window has NO LED claim**
  — the wake/listen blue effect ends with the assist turn, and runCapture
  claims nothing, so the ring goes dark while the dot is recording (same
  claim-scope family as the purple playback bug D-ANN-11a, inverted).
  FIX: claim a capture-side effect (listen-blue) for the window, restore on
  exit paths.
- **D-ANN-17 (OPEN, quirk witnessed): the capture chime and the "Speak now."
  prompt COLLIDE** — chime rides the invoke (D-ANN UX reorder) at the same
  instant the assist pipeline's TTS grab the speaker; the TTS masks the
  chime (live: chime fired in logcat, Jag heard only "Speak now."). FIX:
  stagger the chime ahead of the prompt (or resolve ownership of the first
  ~1s of the capture UX).
- Regression WATCH: the 10:38 GMT silent crash at capture start did NOT
  reproduce on the recalibrated build's first voice announce (full chain
  survived, err.log wrapper armed). Root cause still unidentified — treat
  every future voice announce as a potential reproduction; capture stderr
  stays armed on lounge.

## 19 Oct (session 3) — capture UX rebuild (D-ANN-18 series)

Witnesses (Jag) + commits 2d7cf79…dcb05f6. Constraint found by evidence:
the dot's speaker drains ~2s past the pipeline "turn ended" (HA TTS
chunking + the dot's media queue), the canceller zero-suppresses the feed
the whole time (level-based on the loopback ref; only a 250ms hold tail),
and words spoken INTO that span are destroyed feed-side (live: "Hello
Jasmine" fully absent; NOT a gain problem — ch2 was bleed-dominated).
Raw-mic capture was PROPOSED and DECLINED (Jag: thresholds too coarse on
the raw feed, speculative trade-offs).

- **D-ANN-18: the capture go-cue is the settle moment.** Dark while the
  drain runs, listen-effect + tick AT the first ≥200ms contiguous quiet
  inside the first 6s (announceSettleQuietFrames/SpanFrames). A capture
  with no bleed settles ~200ms in — the cue is effectively instant.
- **D-ANN-18b: the published capture STARTS at settle.** Pre-settle
  frames drive the bail counters but never publish — waiting for the go
  cue no longer bakes ~2s of residue into the recording head.
- **D-ANN-18c: the settle counts lead-in frames too** (its own contiguous
  counter) — the lead-in keeps guarding only the bail counters.
- **D-ANN-18d (prototype 1): silent prompt + purple capture.** The
  automation responds with an EMPTY conversation response ('') — the
  prompt TTS was itself the reason for the ~3s lag chain (pipeline close
  ∥ speaker drain). Capture LED = PURPLE (EffectAnnounce, same family as
  the receiver's playback) to disambiguate intent-blue from action.
- **The go-tick: 80ms ToneVolume at settle** (prototype 2, ACCEPTED by
  witness) — sound+light go-cue, no TTS drain bought back; tick rides the
  published span (~2 frames).
- **D-ANN-12a: nudge-anyway is DEAD.** A 404 whose X-Announce-State is
  'idle' (and any poll timeout) nudges NOTHING — the receiver must never
  light for a slot that will not exist (19 Oct live, twice). The dot's
  serve() publishes the header; HA's _await_capture returns
  (id, captured|aborted|gone|timeout).
- **Single-flight orchestration**: a second echolocal_dropin.announce
  call cancels the previous in-flight one — its 30s poll used to expire
  mid-capture of the newer announce and nudge anyway (overlapping-run
  race, caught in the same live session).
- Residual known edge: an announcement totalling <50 loud frames (<1s)
  aborts by design (min-spoken gate) — and the HEARD<50 case also blocks
  the trailing bail, so the lane holds the full 15s window before the
  abort. Witnessed heard=49. Acceptable for now; revisit if it bites.
