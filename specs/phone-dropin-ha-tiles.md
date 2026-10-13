# HA Lovelace tiles for the phone drop-in dialer (card 9, updated card 16, D-DI-9c/d)

The dashboard tiles ARE the directory — the dialer page has zero discovery
logic. Each tile is a self-contained deep link with the target dot baked in.

CARD 16 GOTCHA (live-tested 13 Oct): EVERY PWA deep link MUST carry
`sig=https://homeassistant.jagbains.org`. With `sig` omitted the page falls
back to POSTing `/call/*` straight at the dot, the browser then fetches the
dot's SELF-SIGNED cert directly and Chromium kills the request with
ERR_CERT_AUTHORITY_INVALID — the call never happens and there is no visible
error besides a dead page. So `sig=` is MANDATORY in the tile URL template,
not optional.

## Tile recipe (copy-paste, no new HACS dep)

button-card is NOT installed (see /config/www/community); everything below
uses Home Assistant built-ins plus the already-present `lovelace-mushroom`,
so the recipe needs no new HACS dependency.

Tiles live on the Family dashboard in a **Drop-in** section (horizontal or
vertical stack, one tile per room/dot). The tile tap navigates the browser
to the page; the page's confirm button is the gate (D-DI-9d: NEVER
auto-dial from the tile).

URL template (MANDATORY parts marked):

```
https://homeassistant.jagbains.org:8123/local/phone-dropin.html
  ?host=<dot-ip>            (required, must have)
  &room=<room-id>           (required, must have)
  &name=<Display Name>      (required, must have)
  &sig=https://homeassistant.jagbains.org   (MANDATORY — see gotcha above)
```

Validated kitchen entry (Jag, two-way audio confirmed): host=192.168.10.99,
room=kitchen, name=Kitchen.

```yaml
type: web-link
name: Call Kitchen
icon: mdi:microphone
url: >-
  https://homeassistant.jagbains.org:8123/local/phone-dropin.html?host=192.168.10.99&room=kitchen&name=Kitchen&sig=https://homeassistant.jagbains.org
```

Equivalent built-in `tile` variant (if you prefer tile cards on the section):

```yaml
type: tile
name: Call Kitchen
icon: mdi:microphone
tap_action:
  action: url
  url_path: >-
    https://homeassistant.jagbains.org:8123/local/phone-dropin.html?host=192.168.10.99&room=kitchen&name=Kitchen&sig=https://homeassistant.jagbains.org
```

A Mushroom (already installed) wraps the same URL with
`tap_action: {action: url, url_path: <same>}` if a nicer look is wanted.

### 2-tile starter for the Drop-in section (apply on the dashboard)

```yaml
type: vertical-stack
cards:
  - type: heading
    heading: Drop-in
    heading_style: title
  - type: web-link
    name: Call Kitchen
    icon: mdi:microphone
    url: >-
      https://homeassistant.jagbains.org:8123/local/phone-dropin.html?host=192.168.10.99&room=kitchen&name=Kitchen&sig=https://homeassistant.jagbains.org
  # Placeholder second room — swap dot IP, room id and display name when the
  # next dot is deployed; everything else stays identical.
  # - type: web-link
  #   name: Call Living Room
  #   icon: mdi:microphone
  #   url: >-
  #     https://homeassistant.jagbains.org:8123/local/phone-dropin.html?host=<dot-ip>&room=<room-id>&name=Living%20Room&sig=https://homeassistant.jagbains.org
```

### Future dots

Same shape: swap `host` (dot IP), `room` (room id for the callee dot) and
`name` (display label). `sig` NEVER changes.

## Per-user note: Hannah

Hannah's phone gets its own pairing-key entry: on her FIRST call the page
asks for a pairing key + her name (caller name is entered per device and
persisted in localStorage under `name:<host>` — never taken from the URL).
Server-side the presented key decides the identity (D-DI-9b anti-spoof).
No special tile per person: tiles are per-ROOM; the caller identity is per
PHONE via the entered name.

## Pairing key

On the FIRST successful dial to a given `host`, the page stores the entered
pairing key in localStorage under `key:<host>`. Each phone holds its own key;
it is entered once per dot.

## HITL (NOT a CHECK)

Mic capture inside the HA companion WebView is the separate human test:
Android companion WebView yes; iOS must open in Safari.