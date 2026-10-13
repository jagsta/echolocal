# HA Lovelace tiles for the phone drop-in dialer (card 9, D-DI-9c)

The dashboard tiles ARE the directory — the dialer page has zero discovery
logic. Each tile is a self-contained deep link with the target dot baked in.

## Tile recipe (copy-paste)

Dashboard → edit → add card → **Web link** card (or a custom `tile` with
`tap_action: url`):

```yaml
type: web-link
name: Call Kitchen
icon: mdi:microphone
url: >-
  http://192.168.10.99:18080/call/phone-dropin
  ?host=192.168.10.99&room=kitchen&name=Kitchen
```

URL shape consumed by the page (`tools/phone-dropin/index.html`, served by the
dot at `/call/phone-dropin`):

```
/call/phone-dropin?host=<ip-or-host>&room=<room-id>&name=<display-name>
```

- `host` (required, validated): the TARGET dot's address to dial. Absent or
  malformed host ⇒ friendly error, page never dials.
- `room` / `name`: display only (button label "Call <name>"); the callee dot
  announces itself, so these never affect the call.
- No `manifest.json` / service worker: lean PWA shape by design (D-DI-9c), the
  tile opens the URL directly (HTTP GET, no fetch from HA).

## Pairing key

On the FIRST successful dial to a given `host`, the page stores the entered
pairing key in localStorage under `key:<host>`. Each phone holds its own key;
it is entered once per dot.

## HITL (NOT a CHECK)

Mic capture inside the HA companion WebView is the separate human test:
Android companion WebView yes; iOS must open in Safari.