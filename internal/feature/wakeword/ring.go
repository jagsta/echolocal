package wakeword

import (
	"fmt"
	"slices"

	esphome "github.com/ygelfand/go-esphome-device"

	"github.com/ygelfand/libcountertop/pkg/assistant/wakesettings"
	setting "github.com/ygelfand/libcountertop/pkg/settings"
	"github.com/ygelfand/libcountertop/pkg/settings/schema"

	"github.com/ygelfand/echolocal/internal/component"
	"github.com/ygelfand/echolocal/internal/config"
)

const ringDomain = "ring"

func (w *WakeWord) ring() *setting.Controls[schema.Wake] {
	var (
		groups []setting.Group
		rows   []setting.Setting[schema.Wake]
		pick   = map[string]*esphome.Select{}
	)
	for n, e := range w.effects {
		groups = append(groups, wakesettings.SlotGroup(n))
		for _, r := range []struct {
			field string
			sel   *esphome.Select
			blank string
			value func(*schema.WakeWord) *string
		}{
			{"effect", e.wake, component.EffectNone, func(v *schema.WakeWord) *string { return &v.Effect }},
			{"thinking_effect", e.thinking, component.EffectDefault, func(v *schema.WakeWord) *string { return &v.ThinkingEffect }},
			{"replying_effect", e.replying, component.EffectDefault, func(v *schema.WakeWord) *string { return &v.ReplyingEffect }},
		} {
			name := wakesettings.SlotName(n, r.field)
			pick[name] = r.sel
			rows = append(rows, effectRow(n, name, r.sel, r.blank, r.value))
		}
	}
	return &setting.Controls[schema.Wake]{
		Table: setting.NewTable(ringDomain, groups, rows, wakesettings.SlotMessages(ringDomain)),
		Read:  func() schema.Wake { return config.Get().Wake },
		Save: func(s setting.Setting[schema.Wake], v string) error {
			sel, ok := pick[s.Name]
			if !ok {
				return fmt.Errorf("%s: no setting called %q", ringDomain, s.Name)
			}
			sel.OnCommand(v)
			return nil
		},
	}
}

func effectRow(
	n int,
	name string,
	sel *esphome.Select,
	blank string,
	value func(*schema.WakeWord) *string,
) setting.Setting[schema.Wake] {
	s := setting.Setting[schema.Wake]{
		Name:     name,
		Kind:     setting.Choice,
		Group:    wakesettings.SlotGroup(n),
		NoEntity: true,
	}
	for _, o := range sel.Options {
		s.Options = append(s.Options, setting.Option{Value: o, Label: o})
	}
	s.Read = func(c *schema.Wake) string {
		if v := c.Slot(n); *value(&v) != "" {
			return *value(&v)
		}
		return blank
	}
	s.Write = func(c *schema.Wake, v string) error {
		if !slices.Contains(sel.Options, v) {
			return fmt.Errorf("%s: no effect called %q", name, v)
		}
		if v == blank {
			v = ""
		}
		words := c.Slots(max(len(c.Words), n+1))
		*value(&words[n]) = v
		c.Words = words
		return nil
	}
	return s
}
