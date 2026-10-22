//go:build !board_biscuit

package chromecast

import (
	"context"

	"github.com/ygelfand/libcountertop/pkg/display/castscreen"
	"github.com/ygelfand/libcountertop/pkg/display/geometry"
	"github.com/ygelfand/libcountertop/pkg/display/surface"
	"github.com/ygelfand/libcountertop/pkg/display/video"
	"github.com/ygelfand/libcountertop/pkg/media/cast"
	"github.com/ygelfand/libcountertop/pkg/media/cast/protocols/primevideo"
	"github.com/ygelfand/libcountertop/pkg/media/cast/protocols/unsupported"
	"github.com/ygelfand/libcountertop/pkg/media/cast/protocols/youtube"
	"github.com/ygelfand/libcountertop/pkg/media/cast/protocols/youtube/unplugged"
	"github.com/ygelfand/libcountertop/pkg/media/castoutput"
	"github.com/ygelfand/libcountertop/pkg/media/castreceiver"
	"github.com/ygelfand/libcountertop/pkg/media/playback"
	"github.com/ygelfand/libcountertop/pkg/media/videostream"
	sharedcomponent "github.com/ygelfand/libcountertop/pkg/runtime/component"
	setting "github.com/ygelfand/libcountertop/pkg/settings"

	"github.com/ygelfand/echolocal/internal/board"
	"github.com/ygelfand/echolocal/internal/component"
	"github.com/ygelfand/echolocal/internal/feature/videoplayer"
	"github.com/ygelfand/echolocal/internal/hardware/display"
	"github.com/ygelfand/echolocal/internal/ui"
)

func configureSettings(r *castreceiver.Receiver) {}

func wants(setting.Group) bool { return true }

func needs() []sharedcomponent.Option {
	return []sharedcomponent.Option{component.Needs(board.Panel)}
}

func helper() *surface.Client { return display.Get().Helper() }

func apps() []cast.Maker {
	return []cast.Maker{
		primevideo.Maker(helper),
		unsupported.Maker,
		youtube.Maker(unplugged.TV(helper)),
	}
}

func pictures() func() playback.Target {
	return func() playback.Target {
		return video.Target(display.Get(), component.Board().MaxFPS)
	}
}

func logo() func() []byte { return ui.Logo().PNG }

func screen() castoutput.Screen {
	return castscreen.New(castscreen.Options{
		NewPage: videoplayer.NewPage,
		NewUnder: func() *videostream.Beneath {
			return &videostream.Beneath{
				Rotation: func() geometry.Orientation { return display.Get().Orientation() },
				Size:     func() (int, int) { return display.Get().Native() },
			}
		},
		Video: func(ctx context.Context, b *videostream.Beneath, s videostream.Stream) (videostream.Report, error) {
			player := video.Player{
				Helper:         display.Get().Helper(),
				SecureDecoders: component.Board().SecureDecoders,
			}
			return player.On(ctx, b, s)
		},
	})
}
