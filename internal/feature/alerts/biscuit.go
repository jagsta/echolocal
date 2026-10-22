//go:build board_biscuit

package alerts

import (
	shared "github.com/ygelfand/libcountertop/pkg/audio/alerts"

	"github.com/ygelfand/echolocal/internal/feature/tones"
)

func configure(a *shared.Alerts) { a.Table = shared.TimerTable(tones.Get().Choices) }
