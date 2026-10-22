//go:build !board_biscuit

package nws

import (
	"sync"

	"github.com/ygelfand/libcountertop/pkg/homeassistant/nws"
	sharedcomponent "github.com/ygelfand/libcountertop/pkg/runtime/component"
	"github.com/ygelfand/libcountertop/pkg/runtime/remember"

	"github.com/ygelfand/echolocal/internal/component"
	"github.com/ygelfand/echolocal/internal/config"
	"github.com/ygelfand/echolocal/internal/feature/homeassistant"
	featureremember "github.com/ygelfand/echolocal/internal/feature/remember"
	"github.com/ygelfand/echolocal/internal/feature/shell"
	"github.com/ygelfand/echolocal/internal/layout"
)

func init() {
	component.Register(sharedcomponent.Device, Get, sharedcomponent.Order(62))
}

var get = sync.OnceValue(func() *nws.NWS {
	return nws.New(nws.Options{
		Controller: homeassistant.Get(),
		Agent:      "(EchoLocal " + layout.Version + ", github.com/ygelfand/echolocal)",
		Settings:   config.WeatherSection,
		Dismissed:  remember.NewSlot[[]string](featureremember.Get(), "weather.dismissed"),
		Redraw:     func() { shell.Get().Redraw() },
	})
})

func Get() *nws.NWS { return get() }
