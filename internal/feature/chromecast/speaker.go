//go:build board_biscuit

package chromecast

import (
	"github.com/ygelfand/libcountertop/pkg/media/cast"
	"github.com/ygelfand/libcountertop/pkg/media/cast/protocols/unsupported"
	"github.com/ygelfand/libcountertop/pkg/media/cast/protocols/youtube"
	"github.com/ygelfand/libcountertop/pkg/media/castoutput"
	"github.com/ygelfand/libcountertop/pkg/media/castreceiver"
	"github.com/ygelfand/libcountertop/pkg/media/playback"
	sharedcomponent "github.com/ygelfand/libcountertop/pkg/runtime/component"
	setting "github.com/ygelfand/libcountertop/pkg/settings"
)

func needs() []sharedcomponent.Option { return nil }

func wants(g setting.Group) bool { return g != castreceiver.PrimeGroup }

func configureSettings(r *castreceiver.Receiver) {
	r.Settings().Table = r.Settings().Table.Without("youtube.livedelay", "youtube.skip")
}

func apps() []cast.Maker { return []cast.Maker{unsupported.Maker, youtube.Maker(nil)} }

func pictures() func() playback.Target { return nil }

func logo() func() []byte { return nil }

func screen() castoutput.Screen { return nil }
