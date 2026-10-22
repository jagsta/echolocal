package commands

import (
	"sync"

	sharedcomponent "github.com/ygelfand/libcountertop/pkg/runtime/component"
	"github.com/ygelfand/libcountertop/pkg/voice"

	"github.com/ygelfand/echolocal/internal/component"
	"github.com/ygelfand/echolocal/internal/config"
	"github.com/ygelfand/echolocal/internal/feature/detect"
	"github.com/ygelfand/echolocal/internal/hardware/mic"
	"github.com/ygelfand/echolocal/internal/layout"
	"github.com/ygelfand/echolocal/internal/parts"
)

func init() {
	component.Register(sharedcomponent.Device, Get, sharedcomponent.Order(41))
}

type Commands struct{ *voice.Listener }

func (c *Commands) Restore(config.Config) { c.Apply() }

func (c *Commands) Close() error {
	c.Stop()
	return nil
}

var get = sync.OnceValue(func() *Commands {
	c := &Commands{voice.NewListener(voice.Options{
		Helper:   layout.VoiceHelper,
		Binary:   parts.Voice(),
		ModelDir: layout.ModelDir,
		Source:   mic.Get(),
		Paused:   detect.Get().Paused,
		Settings: config.VoiceSection,
	})}
	return c
})

func Get() *Commands { return get() }
