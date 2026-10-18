package intercom

// D-ANN (specs/dot-announce-decisions.md, card 1 of 3): glue between the
// announce lanes and echod's live seams, plus the two HA-facing actions.
// Everything here follows wiring.go's discipline: hardware and singleton
// touches live in this file; the lane logic (announce.go, announce_play.go)
// stays seam-injectable and unit-testable without hardware.
//
// The two actions (D-ANN-2 split; orchestration stays HA's, card 2):
//   - dropin_announce (origin): start a capture (room is informational).
//   - dropin_play (receiver): chime + fetch + play the origin's capture.
//
// Assistant-pipeline suspension is COMPOSED here, not replaced: the call
// lane's wake suppression (detect.PauseWhen(s.Busy()) and voice.SetGuard)
// keep running; the announce lanes OR their own clause in via the
// announceSuspended atomic — pipeline suspension holds while EITHER lane
// owns the dot's audio (D-ANN-4 origin capture, D-ANN-10 receiver playback).

import (
	"log/slog"
	"net/http"

	esphome "github.com/ygelfand/go-esphome-device"

	"github.com/ygelfand/echolocal/internal/feature/detect"
	"github.com/ygelfand/echolocal/internal/feature/voice"
	"github.com/ygelfand/echolocal/internal/feature/web"
	"github.com/ygelfand/echolocal/internal/hardware/led"
)

func init() {
	// D-ANN-7/8: the serve lane rides the dot's existing HTTPS web surface.
	// Registered exactly like the P2P offer route (p2p_offer.go's init):
	// lazily resolved through Get() so nothing is constructed at init time,
	// and mounted by BOTH the :80 and :443 listeners (web.Handle).
	web.Handle("GET "+announcePath, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		Get().ann.serve(w, r)
	}))
}

// announcePauseClause is the DETECT pause clause the announce lanes contribute
// (D-ANN-4 capture, D-ANN-10 playback), composed with the call lane's own
// clause inside wireAnnounce. It reads the atomic the lanes flip, so nothing
// here needs an Intercom reference.
func announcePauseClause() bool { return announceSuspended.Load() }

// wireAnnounce installs the announce lane's production seams on the shared
// Intercom. Called once from wire() (wiring.go tail).
func (i *Intercom) wireAnnounce() {
	a := &i.ann

	a.chime = i.chime
	a.play = i.playIntercom
	// The session lane, as announce sees it (D-ANN-13): idle | ringing |
	// in-call. Muted/Joined map to in-call exactly as Session.Busy() draws
	// the busy line — the call lane is "anything not Idle, not Ringing".
	a.callFn = func() string {
		switch i.session.Now() {
		case Idle:
			return "idle"
		case Ringing:
			return "ringing"
		default:
			return "in-call"
		}
	}
	// D-ANN-4: is the assistant turn open? voice.Get().Turn() is the
	// satellite's ONE conversation turn (libcountertop assistant); Busy()
	// holds from evStart until the pipeline returns to idle.
	a.turnFn = func() bool { return voice.Get().Turn().Busy() }
	// D-ANN-11: purple around receiver playback, painted on the SAME claim
	// the call lane uses (one LED, priority-composed) and cleared afterwards
	// so the underlying paint (idle clear, or the call lane's) shows again.
	a.setLED = func(on bool) {
		if on {
			i.led.Play(led.EffectAnnounce, led.Color{})
		} else {
			i.led.Clear()
		}
	}

	// BOTH suppression seams get the announce clause COMPOSED with the call
	// lane's (added here; the call lane's own registrations stay untouched).
	detect.PauseWhen(announcePauseClause)
}

// CaptureWithRoom is the dropin_announce action body: spawn the capture lane
// and answer the lane state so HA's caller can log the acceptance/refusal.
func (i *Intercom) CaptureWithRoom(room string) map[string]any {
	i.Capture(room)
	return map[string]any{"announce": string(i.ann.now())}
}

// PlayFrom is the dropin_play action body.
func (i *Intercom) PlayFrom(origin string) map[string]any {
	i.Play(origin)
	return map[string]any{"announce": string(i.ann.now())}
}

// wireActionsAnnounce appends the two announce actions to the intercom action
// list (called from Actions()).
func (i *Intercom) wireActionsAnnounce(actions []*esphome.Action) []*esphome.Action {
	return append(actions,
		&esphome.Action{
			Name: "dropin_announce",
			Args: []esphome.Arg{{Name: "room"}},
			// Answers, not waiting: Run spawns the capture goroutine and
			// returns the lane state immediately — the API read loop must
			// never wait on a 15s lane (conn.go's handler contract).
			Answers: true,
			Run: func(c esphome.Call) (any, error) {
				room := c.String("room")
				slog.Info("dropin announce", "room", room)
				return i.CaptureWithRoom(room), nil
			},
		},
		&esphome.Action{
			// D-ANN-9/10: the receiver's play nudge. `origin` is the ORIGIN
			// dot's host:port (card 2 resolves targets and hands each
			// receiver its origin); the playback fetches over plain HTTPS —
			// no pairing gate (D-ANN-8).
			Name:    "dropin_play",
			Args:    []esphome.Arg{{Name: "origin"}},
			Answers: true,
			Run: func(c esphome.Call) (any, error) {
				origin := c.String("origin")
				slog.Info("dropin play", "origin", origin)
				return i.PlayFrom(origin), nil
			},
		},
	)
}
