//go:build board_biscuit

package frontend

import "image"

var shot func() (image.Image, error)
