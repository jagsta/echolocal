//go:build !board_biscuit

package settings

import (
	sharedsettings "github.com/ygelfand/libcountertop/pkg/display/settings"
	sharedshell "github.com/ygelfand/libcountertop/pkg/display/shell"

	"github.com/ygelfand/echolocal/internal/config"
	"github.com/ygelfand/echolocal/internal/feature/airplay"
	"github.com/ygelfand/echolocal/internal/feature/bluetooth"
	"github.com/ygelfand/echolocal/internal/feature/sendspin"
	"github.com/ygelfand/echolocal/internal/feature/shell"
)

type feature struct {
	key string

	on   func(config.Config) bool
	set  func(bool)
	page func() *sharedshell.Page
}

var features = []feature{
	{
		key: "features.sendspin",
		on:  func(c config.Config) bool { return c.Sendspin.Enabled },
		set: func(on bool) { sendspin.Get().SetEnabled(on) },
	},
	{
		key: "features.airplay",
		on:  func(c config.Config) bool { return c.AirPlay.Enabled },
		set: func(on bool) { airplay.Get().SetEnabled(on) },
	},
	{
		key: "features.proxy",
		on:  func(c config.Config) bool { return c.Bluetooth.Proxy },
		set: func(on bool) { bluetooth.Get().SetProxy(on) },
	},
}

func featuresPage() *sharedshell.Page {
	return sharedsettings.FeaturesPage(func() []sharedsettings.Feature {
		c := config.Get()
		out := make([]sharedsettings.Feature, 0, len(features))
		for _, f := range features {
			v := sharedsettings.Feature{Key: f.key, Set: f.set}
			if f.on != nil {
				v.On = f.on(c)
			}
			if f.page != nil {
				v.Page = func() sharedshell.View { return f.page() }
			}
			out = append(out, v)
		}
		return out
	}, shell.Get().Push)
}
