package volume

import (
	"sync"

	sharedshell "github.com/ygelfand/libcountertop/pkg/display/shell"
	sharedview "github.com/ygelfand/libcountertop/pkg/display/volume"
	"github.com/ygelfand/libcountertop/pkg/settings/schema"

	"github.com/ygelfand/echolocal/internal/config"
	"github.com/ygelfand/echolocal/internal/feature/feedback"
	"github.com/ygelfand/echolocal/internal/feature/media"
	"github.com/ygelfand/echolocal/internal/feature/shell"
)

var (
	pageOnce sync.Once
	sound    *sharedshell.Page
)

func page() *sharedshell.Page {
	pageOnce.Do(func() {
		sound = sharedview.NewPage(sharedview.PageOptions{
			Levels:    Get(),
			Streams:   config.Streams(),
			MainSteps: media.VolumeSteps,
			Chimes:    chimes{},
			Feedback:  feedback.Get().Feedback,
			Media:     media.Get().Settings(),
			Push:      shell.Get().Push,
		})
	})
	return sound
}

func Page() sharedshell.View { return page() }

type shows interface{ Shows(config.Stream) bool }

func showing(s config.Stream) bool {
	top := shell.Get().Top()
	if top == sharedshell.View(page()) {
		return true
	}
	v, ok := top.(shows)
	return ok && v.Shows(s)
}

type chimes struct{}

func (chimes) Chime() schema.Chime     { return config.Get().Feedback.Chime }
func (chimes) SetChime(c schema.Chime) { feedback.Get().SetChime(c) }
func (chimes) Preview(s schema.Stream) { feedback.Preview(s) }
