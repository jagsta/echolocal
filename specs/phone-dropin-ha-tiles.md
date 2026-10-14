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

## Adding a new Echo unit — runbook (DHCP reservation chosen over DNS, 14 Oct)

Per-dot human cost after autodiscovery (grilled decision: DHCP reservations,
not DNS records — dots only speak mDNS/zeroconf; making unicast DNS records
for them adds machinery nobody needs until 4+ dots force per-dot NPM rules
anyway):

1. **Reserve the IP.** On the UDR controller: DHCP reservation, MAC ←→
   192.168.10.x for the dot's subnet. The dot's MAC is readable from `ha`
   config entries once adopted (step 3) — so if you prefer reserving BEFORE
   letting the device online, take the MAC from the `echolocal install`
   output/adb shell (`cat /proc/idme/mac_addr` equivalent).
   SKIPPING this doesn't break anything immediately — it just risks a silent
   tile breakage if DHCP reassigns the IP later.
2. **Flash + name.** `echolocal install --name <room>` (the room name drives
   the device prefix AND the callee display name in HA). Prints the pairing
   key — keep it.
3. **Adopt in HA.** One click in the ESPHome integration discovery flow
   (Settings → Devices → the new dot shows up automatically); paste the
   pairing key. ~137 entities register themselves. If you skipped step 1,
   grab the dot's MAC from the new esphome config entry (`unique_id`) and do
   the reservation now.
4. **Check the area.** The dot's devices land in an area matching the install
   name automatically; if one lands wrong, move it in the UI (or websocket
   `area_registry/update`).
5. **Regenerate the tiles.** From hermes:
   `/home/jagb/.hermes/installs/7ec44d0e870bd945/environments/f26e4fd4543e4b968079e6cab9e99bbb/venv/bin/python tools/ha/gen_dropin_dashboard.py --pending <any not-yet-provisioned rooms> --out /tmp/dropin.yaml`
   — reads HA's own registries (esphome config entries + device/area
   registries, EchoLocal-manufacturer filtered), emits the full dashboard
   YAML with live tiles for every discovered dot and disabled placeholders
   for the pending list. Paste into the Drop-in dashboard raw editor (or
   hand-patch the one new tile for a surgical change).
6. **Set the pairing key + caller name on each phone** that will dial the new
   room (one-time, per dot per phone) and add the dot-side key entry via
   `ctl settings intercom.phone_keys "jag:...,hannah:..."`.

Every tile the generator emits carries `sig=https://homeassistant.jagbains.org`
(the card-16 gotcha is baked into the generator itself — it CANNOT emit a
sig-less tile).
