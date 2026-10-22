package chromecast

import (
	"github.com/ygelfand/libcountertop/pkg/media/castoutput"
	"github.com/ygelfand/libcountertop/pkg/media/pcm"

	"github.com/ygelfand/echolocal/internal/config"
	"github.com/ygelfand/echolocal/internal/feature/media"
	"github.com/ygelfand/echolocal/internal/hardware/speaker"
)

func NewOutput() *castoutput.Output {
	a := speaker.Sound().Backgrounds()
	return castoutput.New(
		castoutput.Options{
			Queue:    speaker.Get(),
			Rate:     speaker.Rate,
			Channels: speaker.Channels,
			DuckDB:   func() float64 { return config.Get().Media.DuckDB },
			Media:    func() castoutput.Media { return media.Get() },
			Took:     func(p pcm.Producer) { a.Took(p) },
			Gave:     func(p pcm.Producer) { a.Gave(p) },
			Screen:   screen(),
		},
	)
}
