package remember

import (
	"log/slog"
	"sync"

	"github.com/ygelfand/libcountertop/pkg/runtime/remember"

	"github.com/ygelfand/echolocal/internal/layout"
)

var get = sync.OnceValue(func() *remember.Store {
	s, err := remember.Open(layout.RuntimePath)
	if err != nil {
		slog.Warn("reading runtime state", "err", err)
	}
	return s
})

func Get() *remember.Store { return get() }
