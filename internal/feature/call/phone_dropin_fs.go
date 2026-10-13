package call

import "embed"

// Card 7 (D-DI-8, specs/phone-dropin-webrtc.md): the phone drop-in page,
// served at /call/phone-dropin from the dot's web server (and usable straight
// from file:// with no build step). Card 9 (D-DI-9c-d): the page is a
// zero-discovery dialer driven entirely by URLSearchParams (?host=&room=&name=)
// — the HA Lovelace tiles are the directory (no rooms.json fetch) and there is
// no auto-dial. Source of truth for the page lives at
// tools/phone-dropin/index.html; this embedded copy is kept byte-identical.

//go:embed index.html
var phoneDropinFS embed.FS
