package profile

import (
	"github.com/ygelfand/echolocal/internal/board"
	"github.com/ygelfand/echolocal/internal/host/sysimg"
)

var athena = Profile{
	Board:  board.Athena,
	System: sysimg.Athena,

	Disable: checkers.Disable,
	Enable:  checkers.Enable,

	SupportedSDK:         "28",
	UnsupportedSDKReason: "want 28 (Fire OS 7)",
}
