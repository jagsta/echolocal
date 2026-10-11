package config

import (
	"github.com/ygelfand/libcountertop/pkg/settings/schema"

	"github.com/ygelfand/echolocal/internal/board"
	"github.com/ygelfand/echolocal/internal/defaults"
	"github.com/ygelfand/echolocal/internal/layout"
)

type Config struct {
	schema.Shared

	Device     Device     `json:"-"`
	Speaker    Speaker    `json:"speaker"`
	Volume     Volume     `json:"volume"`
	Microphone Microphone `json:"microphone"`
	Ring       Ring       `json:"ring"`
	Intercom   Intercom   `json:"intercom"`
	Diag       Diag       `json:"diag"`
}

func Defaults() Config {
	d := defaults.Current()

	c := Config{
		Shared:     schema.DefaultShared(),
		Speaker:    defaultSpeaker(),
		Volume:     defaultVolume(),
		Microphone: defaultMicrophone(d),
		Ring:       defaultRing(d),
		Intercom:   defaultIntercom(d),
		Diag:       defaultDiag(d),
	}
	c.Visual.Label = layout.Manufacturer
	return c
}

type Device struct {
	Name string
	Addr string

	Board board.Board
}

type Writer struct {
	schema.SharedWriter[Config]
	st *Store
}

func (w Writer) Speaker() SpeakerWriter       { return SpeakerWriter{st: w.st} }
func (w Writer) Volume() VolumeWriter         { return VolumeWriter{st: w.st} }
func (w Writer) Microphone() MicrophoneWriter { return MicrophoneWriter{st: w.st} }
func (w Writer) Ring() RingWriter             { return RingWriter{st: w.st} }
func (w Writer) Intercom() IntercomWriter     { return IntercomWriter{st: w.st} }
func (w Writer) Diag() DiagWriter             { return DiagWriter{st: w.st} }

type Labelled interface{ Label() string }

func Labels[T Labelled](values []T) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		out = append(out, v.Label())
	}
	return out
}

func ByLabel[T Labelled](values []T, label string) (T, bool) {
	for _, v := range values {
		if v.Label() == label {
			return v, true
		}
	}
	var zero T
	return zero, false
}
