//go:build !board_biscuit

package alerts

import (
	shared "github.com/ygelfand/libcountertop/pkg/audio/alerts"
)

func configure(a *shared.Alerts) {}
