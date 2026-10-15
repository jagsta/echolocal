# Decision D-P2P: dot↔dot intercom media goes DIRECT (v2 P2P accelerated), HA-relay mothballed

Grilled with Jag 15 Oct 2026. Status: DECIDED, not yet implemented.
Supersedes the media half of D-DI-4 ("HA is the frame relay"); signalling
stays HA-owned per D-DI-3/5/6. Decisions doc form matches dot-cert-decisions.md.

## Background (why the v1 relay died)

15 Oct live E2E (t_c8ea2b7b evidence): signalling fully works (button accept →
dot sensor in-call → HA accept → one connect event → dropin_connect to both
dots, chimes/LED) but NO AUDIO either way. Two structural defects found:

1. The relay's ingest half was never built: `relay.feed()` has zero production
   callers; runtime.py wires only the send half (`_send_frame` →
   VoiceAssistantAudio{Data} peer push). The probe findings (§4) assumed
   "relaying PCM through HA's assist satellite connections is within what HA
   already does" — but a custom component has no seam to tap the esphome
   AssistSatellite's mic frames.
2. Worse, the dots' perpetual intercom turn gets CONSUMED BY HA's normal voice
   assistant pipeline: during the live call both dots logged
   `heard <speech>` → `replying ...` (STT→LLM→TTS) while in-call. HA treats the
   assist satellite's audio as an ordinary assistant turn — the call audio never
   reaches the relay even if an inlet existed.

Jag chose to accelerate v2 direct P2P (previously reserved post-v1) over
patching the relay inlet. Supported by: the phone↔dot leg already runs direct
Webrtc (pion) validated live (card 14 family), and probe findings §5 already
reserved the go-esphome-device protocol work for "v2 P2P" — that point arrived
early because the v1 prerequisite (HA frame inlet) turned out to be unsound.

## Decisions

- **D-P2P-1: v1-P2P scope is dot↔dot ONLY; the phone path is untouched.**
  Phone↔dot direct Webrtc (internal/feature/call, D-DI-8) is done and validated;
  dot↔dot is the only new leg. Composition: dot calls + phone participants ride
  their own existing transports.
- **D-P2P-2: HA stays in the SIGNALLING loop; media only goes direct.**
  Ring/accept/hang-up/permissions remain HA-owned (session machine, button,
  voice answer — the machinery fixed in t_c8ea2b7b/t_108fc818). On HA's
  `connect` event the `dropin_connect` action gains a `peer=<hostname>` arg;
  the receiving dots then establish the session directly (D-P2P-4). Rationale:
  removing HA from signalling re-diverges the state machines — the exact class
  of bug the session-sensor subscription just fixed. Consequence recorded: a
  dot↔dot call no longer drops if HA restarts (D-DI-4's accepted consequence is
  upgraded mid-roadmap — intentional, logged here).
- **D-P2P-3: multi-way stays on the existing AddParticipant seam + the ERLE
  measurement gate; no redesign now.** Under P2P the multi-way composition
  shifts from "HA fans out" to "caller coordinates peers" (caller opens one
  PeerConnection per participant and mixes inbound; session.AddParticipant is
  the wiring seam). The true multi-way constraint is the endpoint DSP ceiling —
  software AEC + N-stream mixing on armv7 — which IDENTICAL in both
  architectures (each dot sums inbound streams locally regardless). 3+ dots
  multiway is gated on the ERLE measurement (probe findings §3), not on
  architecture.
- **D-P2P-4: media transport = pion WebRTC, SDP over HTTPS :443.** Maximum
  reuse of the validated phone-path machinery (libcountertop
  pkg/media/rtc: TrackLocalStaticSample, card-14 ICE UDP arming
  ECHOLOCAL_CALLUDP 32768:60999 from component Start — verified armed live on
  both dots 15 Oct). No STUN/TURN on this leg (D-P2P-5). go-esphome-device
  stays UNFORKED for now; the custom-message transport reserved by §5 returns
  as the upstream-pr option only once this prototype is stable (consistent with
  the upstream posture in dropin-decisions).
- **D-P2P-5: ICE = host candidates only; caller POSTs the offer to the
  callee's :443, answer inline.** Same LAN, mDNS/UDR-DNS discovery already
  yields both v4+v6 addresses (verified: dual `peer found` addrs on both dots;
  kitchen-dot.jagbains.org resolves on the LAN). No firewall work: the armed
  accept chain already covers ICE checks. Offer direction mirrors the phone
  path (offerer posts, answer returns in the HTTP response — card 13 loopback
  pattern), so no second round-trip is needed. Callee auto-answers: accept was
  already authorized by HA before connect fires, so the callee's HTTP handler
  answers without a second interactive gate.
- **D-P2P-6: dot↔dot leg runs 16kHz MONO Opus** (mic Source native rate, 16k
  mono int16 = the StreamIntercom consumer encoding per relay.go codec note;
  no resample/upmix CPU on armv7). The phone path stays 48k stereo
  (libcountertop rtc.go MimeTypeOpus ClockRate 48000 Channels 2) — separate
  tracks on separate connections, no capability conflict. Any dot↔dot→phone
  bridge work later owns the transcode then.
- **D-P2P-7: the HA-relay send-half is MOTHBALLED behind a transport arg, not
  deleted.** `dropin_connect` gains `transport=p2p|harelays` (default p2p).
  The dots' turn-open work (mute, LED, mic listener, session machine) is
  transport-agnostic and stays. relay.py + _send_frame + their tests remain
  alive but unreached at default. Rationale: one-day-old validated state,
  zero-maintenance fallback if pion hits an armv7 wall precedented by the
  orphaned-intercom-package incident (card 10) — deleting working code the day
  it stopped being the plan has bitten this project.
- **D-P2P-8 (suppression note, no work):** wake suppression during a call
  (D-DI-5) is transport-agnostic and already enforced (detect.PauseWhen(Busy),
  voice.SetGuard) — the P2P leg introduces nothing new; the assistant-hijack
  failure mode disappears because no ESPHome voice turn is used for call audio
  at all.

## Implementation notes (for the executing card)

- Reuse, do not reinvent: pkg/media/rtc PeerConnection setup, card-14
  armICEPorts() (already verified armed on both dots 15 Oct), session machine
  + AddParticipant hook unchanged.
- New inbound: callee runs an auto-answer HTTP endpoint (the existing
  /call/answer route shape, plus gate: accept authorized only when session is
  Ringing/InCall authorized by HA connect — refuse otherwise, mirroring the
  pairing-gate pattern).
- `dropin_connect` arg addition is backward compatible (extra arg ignored by
  the pre-P2P dot binary until both dots are flashed).
- Flash order matters: flash CALLEE first (it must answer offers), then caller,
  then flip HA's transport arg. Single dot + phone only during any interim —
  never attempt dot↔dot with one side pre-P2P.
- VERIFY at flash time: `iptables -S ECHOLOCAL_CALLUDP` armed on both, then
  live: ring → press after 3s floor → connect chime + BOTH LEDs → audio both
  ways → hangup. The failure signature to watch: dots logging `heard/replying`
  during a call means a voice turn is being used — it must not be, post-P2P.

Evidence trail: t_c8ea2b7b comment thread (13:47 run), t_d767ce0a, probe
findings §1/§4/§5, dropin-decisions D-DI-4/5/6/7.