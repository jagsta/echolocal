//go:build !board_biscuit

package drm

import (
	"sync"

	"github.com/ygelfand/libcountertop/pkg/display/surface"
	"github.com/ygelfand/libcountertop/pkg/media/widevine"
	sharedcomponent "github.com/ygelfand/libcountertop/pkg/runtime/component"

	"github.com/ygelfand/echolocal/internal/board"
	"github.com/ygelfand/echolocal/internal/component"
	"github.com/ygelfand/echolocal/internal/feature/dhcp"
	"github.com/ygelfand/echolocal/internal/hardware/display"
)

func init() {
	component.Register(
		sharedcomponent.Device,
		Get,
		sharedcomponent.Order(61),
		component.Needs(board.Panel),
	)
}

var Get = sync.OnceValue(func() *widevine.Provisioner {
	return widevine.New(
		func() *surface.Client { return display.Get().Helper() },
		dhcp.Get(),
		widevine.L1,
	)
})
