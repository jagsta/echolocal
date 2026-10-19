package intercom

// D-ANN-19 (Jag ruling 19 Oct 2026): the receiver's playback is prefaced
// with a spoken "announcement from <source>". There is NO speech engine on
// the dots (tone synthesis only — same reason raw-mic capture was declined),
// so the preface cannot be synthesized at play time. Design accepted by Jag:
//
//   - the ORIGIN prepends the preface PCM to the capture at store time, so
//     the served blob (and therefore every receiver's fetch/play flow,
//     D-ANN-9/10/11 untouched) arrives pre-prefaced — the preface rides
//     inside the receiver's existing pipeline-suspended playback, no extra
//     HA turn, no suspension gap.
//   - the spoken name rides the dropin_announce `source` arg from HA
//     (orchestration stays HA's, D-ANN-2 split); the origin embeds one
//     small TTS asset per known source, generated offline (edge-tts,
//     en-GB-SoniaNeural, 16 kHz mono s16le — mic.Rate's exact shape — with
//     a baked 0.35 amplitude scale so the intro doesn't shout over the
//     room-distance capture levels).
//   - a source with no embedded asset (or an old HA build not sending
//     `source`) serves the capture as-is, logged — the never-wedges rule.

import (
	"embed"
	"strings"

	"github.com/ygelfand/echolocal/internal/hardware/mic"
)

//go:embed preface/*.pcm
var prefaceAssets embed.FS

// prefaceAliases maps accepted source labels onto asset stems beyond their
// own slug. The HA side sends the device_map announce_name ("lounge",
// "kitchen", "Jasmine"); the aliases catch the raw area ids arriving from
// other callers (tests, manual service calls) so both spellings speak.
var prefaceAliases = map[string]string{
	"little_lounge": "lounge",
	"the_lounge":    "lounge",
	"min":           "jasmine",
	"mins_room":     "jasmine",
	"jasmines_room": "jasmine",
}

// prefacePCM loads "announcement from <source>" as int16 samples, or nil
// when no asset covers the source (caller serves the bare capture, logged).
func prefacePCM(source string) []int16 {
	name := slugged(source)
	if name == "" {
		return nil
	}
	stem := slugged(prefaceAliases[name])
	if stem == "" {
		stem = name
	}
	raw, err := prefaceAssets.ReadFile("preface/" + stem + ".pcm")
	if err != nil {
		// Wrong rate/shape would corrupt the lane, so the asset set is
		// generated to the capture's shape exactly (16 kHz mono s16);
		// anything unreadable is a build-time mistake, not runtime.
		return nil
	}
	return decodePCM16LE(raw)
}

// slugged normalizes a source label to an asset-stem candidate: lowercased,
// apostrophes dropped, spaces→underscores. Empty when nothing remains.
func slugged(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "'", "")
	s = strings.ReplaceAll(s, " ", "_")
	return s
}

// prefaceSeconds is the preface's duration for the captured log line.
func prefaceSeconds(pcm []int16) float64 { return float64(len(pcm)) / float64(mic.Rate) }