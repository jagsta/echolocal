//go:build !board_biscuit

package gui

import (
	"github.com/ygelfand/libcountertop/pkg/display/screens"
	"github.com/ygelfand/libcountertop/pkg/display/widgets"

	"github.com/ygelfand/echolocal/internal/feature/apps"
)

func init() {
	extensions = append(extensions, func(look *widgets.UI, o *screens.Options) {
		o.Views = append(o.Views, apps.Get().Screens(look))
	})
}
