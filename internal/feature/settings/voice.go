//go:build !board_biscuit

package settings

import (
	gogui "github.com/go-gui-org/go-gui/gui"

	sharedsettings "github.com/ygelfand/libcountertop/pkg/display/settings"
	sharedshell "github.com/ygelfand/libcountertop/pkg/display/shell"

	"github.com/ygelfand/echolocal/internal/feature/homeassistant"
	"github.com/ygelfand/echolocal/internal/feature/shell"
	"github.com/ygelfand/echolocal/internal/feature/triggers"
)

func voicePage() *sharedshell.Page {
	return sharedsettings.VoicePage(triggers.Get().Controls(), homeassistant.Get(), shell.Get())
}

func init() {
	sections = append(
		sections,
		section{label: "voice.title", glyph: gogui.IconMegaphone, page: voicePage},
	)
}
