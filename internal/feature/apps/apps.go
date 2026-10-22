package apps

import (
	"sync"

	sharedapps "github.com/ygelfand/libcountertop/pkg/display/apps"
	sharedcomponent "github.com/ygelfand/libcountertop/pkg/runtime/component"

	"github.com/ygelfand/echolocal/internal/board"
	"github.com/ygelfand/echolocal/internal/component"
	"github.com/ygelfand/echolocal/internal/config"
	"github.com/ygelfand/echolocal/internal/feature/drawer"
	"github.com/ygelfand/echolocal/internal/feature/shell"
)

func init() {
	component.Register(
		sharedcomponent.Device,
		Get,
		sharedcomponent.Order(35),
		component.Needs(board.Panel),
	)
}

type Apps struct{ *sharedapps.Apps }

var Get = sync.OnceValue(func() *Apps {
	a := &Apps{sharedapps.New(sharedapps.Options{
		Settings: config.AppsSection,
		DeviceID: component.DeviceScreen,
		Shell:    shell.Get(),
	})}
	component.Settings.Add(a.Settings())
	drawer.Get().Add(a.Rail())
	drawer.Get().AddList(a.Dock)
	return a
})

func (a *Apps) Restore(cfg config.Config) { a.Apps.Restore(cfg.Apps) }
