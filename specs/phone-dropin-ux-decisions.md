# Phone Drop-In UX — Grilled Decisions (D-DI-9)

Grilled with Jag 13 Oct 2026. Companion to `specs/phone-dropin-webrtc.md` (D-DI-8)
and `specs/phone-dropin-ux-roadmap.md` (the pre-grill draft — the rungs U1-U4 and
the three open questions there are now RESOLVED below; where the roadmap
contradicts this doc, this doc wins). Implementation sessions implement from
here, do not re-derive.

## D-DI-9a — No listen-in mode, full stop (PRIVACY, supersedes roadmap U2 audio path)

One-way listen-in to a room from a phone is NOT a use case this project will
support, ever. Jag: "it's a privacy nightmare". The only phone mode is a
TWO-WAY call that rings the callee Dot and requires acceptance (D-DI-3 ring
contract). Consequence in code: the `silent_connect` per-pair field that was
"reserved in the schema" since D-DI-3 is REMOVED (field + state-machine
honoring), enforced by test — the privacy guarantee lives in code, not
convention. Roadmap U2's "listen-in via HA tiles over the media_player relay"
audio path is DEAD (never build it).

## D-DI-9b — Key-as-identity (card 8)

The Dot's pairing gate becomes multi-key: `Intercom.PhoneKey string` is
replaced by a name→key map. A phone caller's identity is DERIVED from which
key matched (constant-time compare, server-side); the offer body's `from`
claim is IGNORED for phone sources — the gate overwrites it with
`phone:<matched-name>`. A valid key IS the caller identity: prevents
self-identification spoofing. Dot-to-Dot callers unchanged (no phone: prefix).
Effort note (audited in the grill): config struct + writer ~20 lines, gate
~30, tests mechanical — one card, no protocol/PWA redesign. Card 8 ALSO folds
in the `silent_connect` removal (same permissions/pairing seam, one flight).

## D-DI-9c — HA owns the directory; the PWA is a parameterized dialer (card 9)

The room directory does NOT live on any dot ( Jag caught the flaw in the
draft's rooms.json-on-a-dot shape: which dot serves it? cross-dot sync? it
breaks when that dot is down). Instead: HA Lovelace tiles per room are
SELF-CONTAINED deep links of the form
`http://<dot-host>/call/phone-dropin?host=<kitchen-ip>&room=kitchen&name=Kitchen`.
The dot-served page does ZERO discovery: reads query params, connects to that
host, dials that room. No rooms.json, no service worker/manifest/PWA-install
work in this card (lean shape; polish only if the family bounces off it).
Trade-off accepted: mic capture inside the HA companion WebView works on
Android; iOS historically flaky — iPhones open the link in Safari (works
fine). MUST be tested on Jag's and Hannah's actual phones before card 9 is
authored (HITL, not a CHECK).

## D-DI-9d — No auto-dial on page load

A tile deep-link PRE-SELECTS the room and shows a confirm button; loading the
page never starts ringing on its own. Accidental taps must not blast a room.

## D-DI-9e — Multi-dot/multi-room edge resolved by design (no card)

One dot serving two room names = two tiles, same host, different `room`
values. Room→host resolution happens in the tile (D-DI-9c), never in the PWA.
Card 6's acceptance parity carries the rest.

## D-DI-9f — Permissions: default-allow stands, no speculative restrictions

Phone sources ride the D-DI-3 per-pair map with the same default
allow-household; adding a household member later is pure `ctl settings` data
(key entry) + `set_permission` calls — never code. Tighten only when a real
need appears.

## Carding

- Card 8 — keyed phone identity + silent_connect removal (D-DI-9a/9b).
  CHECKs: greppable (map type in config, server-side From override in gate,
  `silent_connect` grep COUNTS ZERO occurrences in custom_components), plus
  pytest on Hermes venv python: spoofed-from rejected/overridden, unknown key
  rejected, no key configured still fail-closed.
- Card 9 — HA-tile-parameterized dialer page (D-DI-9c/9d). CHECKs: page reads
  host/room/name from query params only (no rooms.json, no directory fetch,
  no localStorage directory), confirm button present (grep), plus a
  state-machine test that no path transitions without the explicit call
  action. HITL: deep-link open + mic permission on both Jag's and Hannah's
  phones.
- Both cards on the hil-framework board per the accepted recipe; they must
  not break card-6 acceptance-parity tests (callee UX byte-identical).