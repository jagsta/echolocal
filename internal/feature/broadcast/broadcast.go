package broadcast

import (
	"context"
	"net/http"
	"sync"

	"github.com/ygelfand/libcountertop/pkg/audio/tone"
	shared "github.com/ygelfand/libcountertop/pkg/media/broadcast"

	"github.com/ygelfand/echolocal/internal/config"
	"github.com/ygelfand/echolocal/internal/feature/alarm"
	"github.com/ygelfand/echolocal/internal/feature/call"
	"github.com/ygelfand/echolocal/internal/feature/discovery"
	"github.com/ygelfand/echolocal/internal/feature/tones"
	"github.com/ygelfand/echolocal/internal/feature/voice"
	"github.com/ygelfand/echolocal/internal/feature/web"
	"github.com/ygelfand/echolocal/internal/hardware/mic"
	"github.com/ygelfand/echolocal/internal/hardware/speaker"
)

var Get = sync.OnceValue(func() *shared.Broadcaster {
	return shared.New(shared.Dependencies{
		Settings: config.CallSection,
		Peers:    discovery.Get(),
		Sound:    device{},
		Tones:    tones.Get(),
		Turns:    voice.Get(),
		Busy:     busy,
		Messages: messages(),
		Client:   discovery.Client(),
	})
})

func init() {
	web.HandlePeer(
		"POST "+shared.Path,
		discovery.Home(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			Get().Handler().ServeHTTP(w, r)
		})),
	)
}

func busy() bool {
	_, calling := call.Get().Now()
	return calling || alarm.Get().Ringing()
}

type device struct{}

func (device) Frames() (<-chan []int16, func()) { return mic.Get().Listen("broadcast") }

func (device) Play(pcm []int16) { speaker.Get().PlayVoice(pcm) }

func (device) Claim(name string, run func(context.Context) error) {
	speaker.Sound().
		Claim(name, func(ctx context.Context, _ *speaker.Player) error { return run(ctx) })
}

func (device) Tone(s config.Stream, level float64, snd tone.Sound) {
	speaker.Sound().Sound(s, level, snd)
}
