//go:build !board_biscuit

package gui

import (
	"github.com/ygelfand/libcountertop/pkg/display/cameraview"
	"github.com/ygelfand/libcountertop/pkg/display/screens"
	"github.com/ygelfand/libcountertop/pkg/display/widgets"

	"github.com/ygelfand/echolocal/internal/feature/apps"
	"github.com/ygelfand/echolocal/internal/feature/cameras"
	"github.com/ygelfand/echolocal/internal/feature/homecontrol"
)

func init() {
	extensions = append(extensions, func(_ *widgets.UI, o *screens.Options) {
		apps.Get().Add(cameras.Get().App())
		for _, a := range homecontrol.Get().Apps() {
			apps.Get().Add(a)
		}
		o.Views = append(o.Views, cameras.Get().Screen)
		if o.Boards == nil {
			o.Boards = map[string]screens.Board{}
		}
		o.Boards[cameraview.TabKind] = screens.Board{
			Glyph: cameras.Get().Glyph,
			Draw:  cameras.Get().Board,
		}
	})
}
