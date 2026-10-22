//go:build !board_biscuit

package triggers

import (
	"github.com/ygelfand/echolocal/internal/board"
	"github.com/ygelfand/echolocal/internal/component"
	"github.com/ygelfand/echolocal/internal/feature/commands"
)

func commandNames() []string {
	if !component.Board().Has(board.Panel) {
		return nil
	}
	out := []string{"radar"}
	if component.Board().CameraWidth > 0 {
		out = append(out, "mirror")
	}
	return out
}

func applyCommands() { commands.Get().Apply() }
