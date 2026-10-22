//go:build !board_biscuit

package broadcast

import (
	shared "github.com/ygelfand/libcountertop/pkg/media/broadcast"

	"github.com/ygelfand/echolocal/internal/board"
	"github.com/ygelfand/echolocal/internal/component"
	"github.com/ygelfand/echolocal/internal/feature/message"
)

func messages() shared.Messages {
	if !component.Board().Has(board.Panel) {
		return nil
	}
	return message.Get()
}
