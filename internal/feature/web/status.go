package web

import (
	"github.com/ygelfand/echolocal/internal/status"
	"github.com/ygelfand/libcountertop/pkg/system/metrics"

	"github.com/ygelfand/echolocal/internal/component"
	"github.com/ygelfand/echolocal/internal/config"
	"github.com/ygelfand/echolocal/internal/feature/timesync"
	"github.com/ygelfand/echolocal/internal/hardware/wifi"
	"github.com/ygelfand/echolocal/internal/layout"
)

var thermal = []status.Thermal{
	{Name: "CPU", Zone: "deca-cpu-max-step"},
	{Name: "GPU", Zone: "gpu0-usr"},
}

func page() status.Page {
	c := config.Get()
	r := metrics.Reader{}

	return status.Page{
		Name:    c.Device.Name,
		Model:   c.Device.Board.Model,
		Version: layout.VersionString(),
		Adopted: Adopted(),
		Parts:   status.Parts(component.Default().Progress()),
		Groups: []status.Group{
			status.Network(wifi.Get().Network(), wifi.Get().MAC(), r),
			status.Clock("", timesync.Get().Zone()),
			status.Screen(c.Screen),
			volumes(c),
			status.System(r, layout.StateDir, thermal),
		},
	}
}

func volumes(c config.Config) status.Group {
	rows := make([]status.Row, 0, 4)
	for _, s := range []config.Stream{
		config.StreamMedia, config.StreamAlerts, config.StreamVoice, config.StreamFeedback,
	} {
		rows = append(rows, status.Row{Name: s.Label(), Value: status.Percent(c.Volume.Level(s))})
	}
	return status.Group{Title: "Volume", Rows: rows}
}
