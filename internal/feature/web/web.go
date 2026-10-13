package web

import (
	"context"
	"log/slog"
	"sync"

	sharedcomponent "github.com/ygelfand/libcountertop/pkg/runtime/component"
	libweb "github.com/ygelfand/libcountertop/pkg/runtime/web"

	"github.com/ygelfand/echolocal/internal/android/firewall"
	"github.com/ygelfand/echolocal/internal/component"
	"github.com/ygelfand/echolocal/internal/config"
	"github.com/ygelfand/echolocal/internal/layout"
)

func init() {
	component.Register(
		sharedcomponent.Network,
		func() *server { return &server{Get()} },
		sharedcomponent.Order(50),
	)
	// Card 11: the HTTPS mirror listener serves the same routes on :443.
	component.Register(
		sharedcomponent.Network,
		func() *tlsListener { return TLSGet() },
		sharedcomponent.Order(55),
	)
}

const Port = 80

var get = sync.OnceValue(func() *libweb.Server {
	return libweb.New(
		libweb.Options{Port: Port, APIPort: layout.Port, Adopted: Adopted, Page: page},
	)
})

func Get() *libweb.Server { return get() }

type server struct{ *libweb.Server }

func (s *server) Run(ctx context.Context) error {
	if err := firewall.Open(firewall.Web, Port); err != nil {
		slog.Error("opening the web port failed", "port", Port, "err", err)
	}
	return s.Server.Run(ctx)
}

func Adopted() bool { return config.Get().API.Adopted }
