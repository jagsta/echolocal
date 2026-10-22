package call

import (
	"context"
	"net/http"
	"sync"

	sharedcall "github.com/ygelfand/libcountertop/pkg/media/call"
	sharedcomponent "github.com/ygelfand/libcountertop/pkg/runtime/component"

	"github.com/ygelfand/echolocal/internal/component"
	"github.com/ygelfand/echolocal/internal/config"
	"github.com/ygelfand/echolocal/internal/feature/detect"
	"github.com/ygelfand/echolocal/internal/feature/discovery"
	"github.com/ygelfand/echolocal/internal/feature/media"
	"github.com/ygelfand/echolocal/internal/feature/tones"
	"github.com/ygelfand/echolocal/internal/feature/web"
	"github.com/ygelfand/echolocal/internal/hardware/buttons"
)

type componentCall struct{ *sharedcall.Calls }

// Card 14b: arm the ICE UDP firewall window at component START, not at Go
// init — Amazon's firewall.sh runs `$IPTABLES --flush` (whole filter table)
// when the boot completes, after which init-armed rules are gone (live
// finding 14 Oct: post-powercycle boots left ECHOLOCAL_CALLUDP empty).
// Every chain armed by its feature's component Run survives; the runtime
// starts components after boot completion, like web.Run's Open.
func (c *componentCall) Run(ctx context.Context) error {
	armICEPorts()
	return c.Calls.Run(ctx)
}

func (c *componentCall) Restore(v config.Config) { c.Calls.Restore(v.Call) }

var once sync.Once
var shared *sharedcall.Calls

func Get() *sharedcall.Calls {
	once.Do(func() {
		d := sharedcall.Dependencies{
			Settings: config.CallSection,
			Peers:    discovery.Get(),
			Port:     web.Port,
			Secure:   web.SecurePort,
			Client:   discovery.Client(),
			Media:    media.Get(),
			Sound:    device{},
			Tones:    tones.Get(),
		}
		front(&d)
		shared = sharedcall.New(d)
		configureSettings(shared)
		component.Settings.Add(shared.Controls())
	})
	return shared
}

func init() {
	component.Register(
		sharedcomponent.Network,
		func() *componentCall { return &componentCall{Get()} },
		sharedcomponent.Order(62),
	)
	// Card 14: arm the firewall accept for pion's ephemeral ICE UDP ports.
	armICEPorts()
	// Card 7 (D-DI-8): the phone PWA reaches the same handlers through the
	// pairing gate; the phone routes stay plain-HTTP under the pairing gate.
	gate := newPhoneGate(Get())
	web.Handle("POST /call/offer", gate.PhoneOfferHandler())
	web.Handle("POST /call/answer", gate.PhoneAnswerHandler())
	web.Handle("POST /call/end", gate.PhoneEndHandler())
	web.Handle("GET /call/phone-dropin", http.StripPrefix("/call/phone-dropin", http.FileServer(http.FS(phoneDropinFS))))
	// upstream peer-security routes: secure peer port for device-to-device.
	web.HandlePeer("POST /call/offer", discovery.Home(Get().OfferHandler()))
	web.HandlePeer("POST /call/answer", discovery.Home(Get().AnswerHandler()))
	web.HandlePeer("POST /call/end", discovery.Home(Get().EndHandler()))
	detect.PauseWhen(Get().PausesWake)
	buttons.Get().Take(take)
}

func take(e buttons.Event) bool {
	if e.Kind != buttons.Tap {
		return false
	}
	if c, ok := Get().Now(); !ok || c.State != sharedcall.Ringing {
		return false
	}
	switch e.Name {
	case buttons.Action:
		Get().AnswerAudio()
	case buttons.VolumeDown:
		Get().Hangup()
	default:
		return false
	}
	return true
}
