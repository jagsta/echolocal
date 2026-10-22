//go:build !board_biscuit

package radio

import (
	"sync"

	"github.com/ygelfand/libcountertop/pkg/homeassistant/radio"

	"github.com/ygelfand/echolocal/internal/config"
	"github.com/ygelfand/echolocal/internal/feature/homeassistant"
	"github.com/ygelfand/echolocal/internal/feature/shell"
	"github.com/ygelfand/echolocal/internal/layout"
)

var get = sync.OnceValue(func() *radio.Radio {
	return radio.New(radio.Options{
		Controller: homeassistant.Get(),
		MAC: func() string {
			mac, _ := layout.FactoryMAC()
			return mac
		},
		Settings: config.RadioSection,
		Redraw:   func() { shell.Get().Redraw() },
	})
})

func Get() *radio.Radio { return get() }
