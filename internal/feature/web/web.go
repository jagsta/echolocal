package web

import (
	"context"
	"log/slog"
	"net/http"
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

const (
	Port       = 80
	SecurePort = 8443
	PublicPort = 443
)

var get = sync.OnceValue(func() *libweb.Server {
	return libweb.New(
		libweb.Options{Port: Port, APIPort: layout.Port, Adopted: Adopted},
	)
})

func Get() *libweb.Server { return get() }

type server struct{ *libweb.Server }

func (s *server) Run(ctx context.Context) error {
	// Card 11: Open is flush-then-add per call, so opening Web with ALL ports
	// here (not via the TLS component's own Open) avoids the last-writer-wins
	// race that drops a port when the web server starts after the TLS listener.
	if err := firewall.Open(firewall.Web, Port, SecurePort, PublicPort); err != nil {
		slog.Error(
			"opening the web ports failed",
			"ports",
			[]int{Port, SecurePort, PublicPort},
			"err",
			err,
		)
	}
	return s.Server.Run(ctx)
}

// Handle is defined in tls.go (package-level choke point: CORS wrap + TLS
// mirror registration). Upstream's plain delegate is intentionally not
// redeclared here.

func HandlePeer(pattern string, h http.Handler) { Get().HandlePeer(pattern, h) }
func Adopted() bool                             { return config.Get().API.Adopted }
