//go:build !board_biscuit

package actions

import (
	sharedha "github.com/ygelfand/libcountertop/pkg/homeassistant"

	"github.com/ygelfand/echolocal/internal/feature/states"
)

func (a *Actions) followIntegration() {
	states.Get().Follow(sharedha.VersionEntity, "").Reported.Listen(func(string) { a.Declare() })
}
