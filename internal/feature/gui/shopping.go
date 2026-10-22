//go:build !board_biscuit

package gui

import (
	gogui "github.com/go-gui-org/go-gui/gui"

	"github.com/ygelfand/libcountertop/pkg/display/screens"
	"github.com/ygelfand/libcountertop/pkg/display/shopping"
	"github.com/ygelfand/libcountertop/pkg/display/widgets"

	"github.com/ygelfand/echolocal/internal/feature/apps"
	"github.com/ygelfand/echolocal/internal/feature/shell"
	"github.com/ygelfand/echolocal/internal/feature/todo"
)

func init() {
	extensions = append(extensions, func(look *widgets.UI, o *screens.Options) {
		list := todo.Get().Todo
		shop := shopping.New(shopping.Options{
			UI:    look,
			Shell: shell.Get(),
			Todo:  list,
			Gate:  func(w *gogui.Window) gogui.View { return Get().Gate(w) },
		})
		apps.Get().Add(shop.App())
		o.Todo = list
		o.Views = append(o.Views, shop.Screen)
	})
}
