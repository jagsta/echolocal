//go:build !board_biscuit

package radar

import (
	"sync"

	"github.com/ygelfand/libcountertop/pkg/radar"

	"github.com/ygelfand/echolocal/internal/config"
	"github.com/ygelfand/echolocal/internal/feature/homeassistant"
	"github.com/ygelfand/echolocal/internal/feature/shell"
	"github.com/ygelfand/echolocal/internal/layout"
)

var get = sync.OnceValue(func() *radar.Radar {
	return radar.New(radar.Options{
		Home:     homeassistant.Get().Home,
		Agent:    "(EchoLocal " + layout.Version + ", github.com/ygelfand/echolocal)",
		Settings: config.WeatherSection,
		Redraw:   func() { shell.Get().Redraw() },
		CacheDir: layout.TileDir,
	})
})

func Get() *radar.Radar { return get() }
