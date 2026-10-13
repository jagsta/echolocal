# EchoLocal Drop-In Intercom — HA custom component (card 3)

HA-side call-state owner + PCM frame relay for two-way drop-in intercom
(Echo Dot Gen 2 running echod). Implements D-DI-3..6 from
`specs/dropin-decisions.md` per the probe verdict in
`specs/dropin-probe-findings.md` (v1 rides existing `VoiceAssistantAudio`
frames over each device's ESPHome native API connection).

## Layout

- `const.py` — decisions constants (3s ring floor, 30-min kill-switch, permission modes).
- `state_machine.py` — pure, transport-free call state machine: per-pair permissions (default allow-household), busy-reject, deterministic mutual-dial winner (lower device-id), enforced ring floor, reconnect-once on drop, max-duration GC, and a star-topology session model with an endpoint LIST (`add_participant`, no hardcoded pairwise bridge — 3-way roadmap constraint from specs 44dacef).
- `relay.py` — `StarRelay`: one relay per session, fan-out to N-1 peers. Transport is injected; tests use a mock. Production send maps to native-API `VoiceAssistantAudio{Data}` pushes.
- `runtime.py` — HA glue: services, state-machine events to chime/media actions, media pause/resume via HA media_player entities (D-DI-5: pause, not duck).
- `select.py` — per-room permission config entity (D-DI-3).
- `tests/` — 17 unit tests: ring floor, busy reject + clear, mutual dial, permission default, reconnect-once, kill-switch, star relay fan-out (2 and 3 endpoints).

## Services

- `echolocal_dropin.call` (caller_device, caller_room, callee_device, callee_room, session_id?)
- `echolocal_dropin.accept` (session_id, device) — refused before the 3s floor
- `echolocal_dropin.hangup` (session_id, device?)
- `echolocal_dropin.set_permission` (source, target, mode)

## Testing

System python per hil-framework convention:

```
/usr/bin/python3 -m pytest custom_components/echolocal_dropin/tests/ -q
```