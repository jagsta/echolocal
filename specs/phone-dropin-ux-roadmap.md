# Phone Drop-In UX Roadmap (D-DI-9, post-card-7)

Status: roadmap draft 13 Oct 2026, companion to specs/phone-dropin-webrtc.md. Driver: Jag — "having to hit the dot on a browser individually isn't a very wife-friendly UX". Goal: phone drop-in must be a HOME-SCREEN TAP AWAY, room-first, zero URLs and zero pairing keys at use time — matching how the Alexa app feels.

Design north star: the PWA keeps ONE pairing-key entry (per browser, once at install) and remembers everything else in localStorage (dot IPs, chosen room names, last dialed). Rooms, not IPs, are the entire UI.

## Rungs, smallest first

### U1 — Room-first PWA (fork-local, smallest step)
Current page dials a fixed/typed dot. Change to:
- A room picker (kitchen, living-room…) stored in localStorage, mapping room → dot IP (one file `rooms.json` served from the page's static set; same shape as echolocal_dropin/area_aliases.json).
- Big tiles: tap "Kitchen" = one tap dial. Buttons: hang up, mute.
- Install as PWA (manifest + service worker): home-screen icon, opens full-screen, offline-capable page shell. iOS Safari and Android both support add-to-home-screen; iOS has no install prompt — a one-time "Share → Add to Home Screen" hint on first open.
Effort: small, fork-local, no echod changes. This alone covers "wife friendly" if the phone is hers and the browser is Chrome/Safari.

### U2 — HA-lovabled surface (no separate page to find)
The family's front door is Home Assistant, not a PWA. Serve the drop-in client INSIDE HA so it appears in the HA app:
- Host the PWA at a dot URL as today, but embed it in the HA Companion app via a WebView card (webpage card pointing at the dot's /call/phone-dropin page) on the Family dashboard tab.
- Or native: HA Lovelace tiles calling `echolocal_dropin.call` with caller_device="phone" per D-DI-8's phone peer shape; audio then needs the mic route to the dot — that path does NOT exist via Lovelace alone (HA companion can't yet pipe mic into a WebRTC peer natively), so tile-driven dialling answers via the callee's SPEAKER AUTO-OPEN (silent_connect, currently reserved in D-DI-3!) — i.e. the tile calls `echolocal_dropin.call`; the dot answers automatically after the 3s ring floor with a chime; the phone's speaker plays the dot's audio through the HA media_player bridge (v1's media_player relay), NOT WebRTC. That gives "tap a tile on the HA dashboard to listen INTO a room" with zero new clients — a Listen-In, not a full duplex call. Two-way talk still goes through the PWA route.
- RECOMMENDATION: do the silent_connect listen-in mode via HA tiles here, and keep two-way talk on the PWA. This matches real family usage: listening into a room is the common case ("what's happening in the kitchen"), full conversation needs the dot anyway.

### U3 — Multiple dots / cross-phone parity
- rooms.json grows a dot registry with health pings (which dots are live); picker hides dead rooms.
- Sharing the PWA betweenJag's and Hannah's phones: pairing key is per-browser (localStorage) — Hannah installs her own key, or a small config page generates one from `ctl settings intercom.phone_key` at setup. This stays per-user so we don't put a shared secret in a shared page.
- Permission-map side (D-DI-3): phone peers are `phone:<name>`, so setting `set_permission source=phone:hannah target=kitchen` per-pair governs who can dial which room without touching code.

### U4 — Native-app feel (optional, only if PWA isn't enough)
- iOS Shortcut instead of PWA: a Shortcut calls the HA service (`echolocal_dropin.call`) or posts to /call/offer with getUserMedia... iOS Shortcuts can't do WebRTC mic capture; so a Shortcut can only do the LISTEN-IN mode (U2 HA tile path). Realistic on iOS: Shortcut tile for listen-in + PWA for talk.
- Flutter/React Native app wrapper embedding the PWA into a real app with home-screen+widget — only worth it if the family bounces off the PWA.

## Decisions to grill (D-DI-9)
- D-DI-9a: U2 listen-in via HA tiles uses silent_connect (currently a reserved field in D-DI-3) — activating it reuses v1's media_player relay for the callee→phone direction. Confirm Jag is happy for the FIRST phone-side mode to be listen-in-only, with two-way staying on the PWA.
- D-DI-9b: pairing key sharing model: one key per phone user (Jag's, Hannah's), generated at setup via the dot CLI — versus a single household key.
- D-DI-9c: does Hannah's usage matter enough to justify U2's HA-tile work before U1's PWA polish? Recommendation: do U1 first (tiny), ship U2's listen-in tile in parallel as the second card, defer U3/U4.

## Carding
- Card 8: U1 — room-first PWA + manifest/service-worker installability. CHECK: greppable (rooms.json schema + localStorage keys + manifest present).
- Card 9: U2 — silent_connect listen-in over the HA relay + Lovelace tiles on the Family dashboard. CHECK: state-machine test asserting silent_connect bypasses the answer-wait (but keeps the 3s chime floor!) + service schema updated.
- Card 10 (later): U3 multi-dot health + per-key permission wiring.
Cards go on the hil-framework board per the accepted recipe; cards 9+ must NOT break the v1 relay or the card-6 acceptance-parity tests.