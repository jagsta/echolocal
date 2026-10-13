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
	// Card 11: Open is flush-then-add per call, so opening Web with BOTH ports
	// here (not via the TLS component's own Open) avoids the last-writer-wins
	// race that drops :443 when the web server starts after the TLS listener.
	if err := firewall.Open(firewall.Web, Port, TLSPort); err != nil {
		slog.Error("opening the web port failed", "port", Port, "err", err)
	}
	return s.Server.Run(ctx)
}

func Adopted() bool { return config.Get().API.Adopted }
