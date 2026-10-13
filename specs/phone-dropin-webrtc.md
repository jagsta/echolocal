# Phone Drop-In via WebRTC — Feature Spec (D-DI-8)

Status: proposed 13 Oct 2026. Companion to `specs/dropin-decisions.md` (D-DI-1..7) — this doc extends the intercom to phone/web callers WITHOUT changing the callee-side acceptance UX.

## Requirement (Jag)

The user-facing feature the Alexa app provides today — drop in on an Echo device FROM A MOBILE — must be replicated. Key constraint: **the callee's acceptance experience (3s ring floor, chime + LED ring, button answer/hang-up, busy handling) must be IDENTICAL regardless of the caller's device** — another echolocal Dot, or the phone/web client.

## Audit finding (13 Oct 2026)

The architecture already delivers this. Call acceptance lives ENTIRELY on the callee side, in `libcountertop/pkg/media/call` (pion/webrtc v4), wrapped by `internal/feature/call/`:

- Incoming offer → callee enters `Ringing` → chime + LED + button handling (`take()`: tap Action = AnswerAudio, tap VolumeDown = Hangup). Caller device is invisible to this path.
- WebRTC HTTP signalling is device-agnostic by construction: `POST /call/offer`, `/call/answer`, `/call/end` on echod's web port.
- A browser/phone is pion's NATIVE peer (getUserMedia + RTCPeerConnection) — no Sendspin client, no HA relay, no new protocol needed for the phone side.
- Sendspin (also present) is playback-only in libcountertop (`pkg/audio/sendspin` has no mic/capture role) — the WebRTC call stack, not Sendspin, is the transport for phone drop-in.
- Privacy gating (`Privacy.MicMuted()`) sits in the shared call layer, so phone-originated calls honor the Dot's mute switch.

## What needs building

1. **Phone PWA client** (fork-local, `tools/phone-dropin/` or a HA panel page):
   - Single HTML page: getUserMedia(audio) + RTCPeerConnection → offer → `POST http://<dot>/call/offer` → answer SDP returned → audio flows.
   - Add to device home screen; buttons: hang-up (POST `/call/end`), mute (toggle track.enabled).
   - Auth: the dot's web port currently accepts any origin — pair with a pairing key stamped at install (echolocal install flow already prints a pairing key) before exposing to LAN-wide browser use.
2. **Peer identity** (small echod tweak): incoming phone offer must surface as a known peer name ("Jag's phone") in the callee's ring announcement. Either register the phone in echod's peers/discovery config, or extend the offer payload to carry a display name the callee announces.
3. **v1 duplex note**: phone caller is full-duplex by nature, but on the CALLEE the v1 half-duplex VAD-squelch session layer (D-DI-2) applies identically — no special case.

## Decision to grill (D-DI-8 candidates)

- D-DI-8a: transport = WebRTC direct to `/call/offer` (this doc's shape) vs HA-relayed (phone calls an HA service that proxies to the relay component). Direct is simpler and keeps the acceptance path byte-identical; HA-relayed adds nothing for a single phone.
- D-DI-8b: auth posture for the dot's web endpoints if phone clients join the LAN (pairing key vs LAN-trust).
- D-DI-8c: does the phone count as a peer under the per-pair permission map (D-DI-3)? Recommend yes — source = "phone:<name>".

## Cards (hil-framework loop board, native recipe)

Cards 6–7 on `feat/dropin-intercom`, sequential:

- **Card 6 — device side: phone-peer acceptance parity.** echod accepts a WebRTC offer from an unregistered web client, announces/rings identically, peer name configurable. CHECK: grep on `internal/feature/call` wiring + python state-machine test asserting an unknown-peer offer enters Ringing with the same floor.
- **Card 7 — phone PWA client + pairing.** Static page served or hosted; verify by a scripted browser (Playwright) or curl-SDP probe dropping in on the pilot Dot; acceptance evidence = callee state machine reaches CallActive after `/call/answer`.

Loop-card recipe (convention): body carries `Executor: <profile>` + `Goal: echolocal#feat-dropin-intercom` + `<!-- hil:criteria:begin/end -->` stamped CHECKs; pytest CHECKs use the Hermes venv python absolute path. Cards live on the `hil-framework` board; hardware-dependent verification (real ring chime on the Dot) is HITL — judge criteria must stick to greppable/state-machine surfaces.