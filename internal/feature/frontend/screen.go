//go:build !board_biscuit

package frontend

import (
	"image"

	"github.com/ygelfand/libcountertop/pkg/runtime/control/screencmd"

	"github.com/ygelfand/echolocal/internal/hardware/display"
)

var shot = func() (image.Image, error) { return screencmd.Composed(display.Get()) }
