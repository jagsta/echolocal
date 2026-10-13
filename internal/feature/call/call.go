package call

import (
	"net/http"
	"sync"

	sharedcall "github.com/ygelfand/libcountertop/pkg/media/call"
	sharedcomponent "github.com/ygelfand/libcountertop/pkg/runtime/component"

	"github.com/ygelfand/echolocal/internal/component"
	"github.com/ygelfand/echolocal/internal/config"
	"github.com/ygelfand/echolocal/internal/feature/detect"
	"github.com/ygelfand/echolocal/internal/feature/discovery"
	"github.com/ygelfand/echolocal/internal/feature/media"
	"github.com/ygelfand/echolocal/internal/feature/web"
	"github.com/ygelfand/echolocal/internal/hardware/buttons"
)

type componentCall struct{ *sharedcall.Calls }

func (c *componentCall) Restore(v config.Config) { c.Calls.Restore(v.Call) }

var once sync.Once
var shared *sharedcall.Calls

func Get() *sharedcall.Calls {
	once.Do(func() {
		d := sharedcall.Dependencies{
			Settings: config.CallSection,
			Peers:    discovery.Get(),
			Port:     web.Port,
			Media:    media.Get(),
			Sound:    device{},
		}
		front(&d)
		shared = sharedcall.New(d)
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
	// Card 7 (D-DI-8): the phone PWA reaches the same handlers through the
	// pairing gate; Dot-to-Dot traffic passes through unwrapped.
	gate := newPhoneGate(Get())
	web.Handle("POST /call/offer", gate.PhoneOfferHandler())
	web.Handle("POST /call/answer", gate.PhoneAnswerHandler())
	web.Handle("POST /call/end", gate.PhoneEndHandler())
	web.Handle("GET /call/phone-dropin", http.StripPrefix("/call/phone-dropin", http.FileServer(http.FS(phoneDropinFS))))
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
