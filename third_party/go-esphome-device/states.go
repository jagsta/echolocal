package esphomedevice

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

type State struct {
	Object string          `json:"id"`
	Value  json.RawMessage `json:"state"`
}

type Description struct {
	Object string          `json:"id"`
	Kind   string          `json:"kind"`
	Info   json.RawMessage `json:"info"`
}

var stateJSON = protojson.MarshalOptions{UseProtoNames: true, EmitUnpopulated: true}

func (e *Entities) Listen(fn func(State)) (cancel func()) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.listeners == nil {
		e.listeners = map[int]func(State){}
	}
	id := e.nextListener
	e.nextListener++
	e.listeners[id] = fn
	return func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		delete(e.listeners, id)
	}
}

func (e *Entities) States() []State {
	var out []State
	for _, ent := range e.all() {
		if st, ok := stateOf(ent.Object(), ent.state()); ok {
			out = append(out, st)
		}
	}
	return out
}

func (e *Entities) Descriptions() []Description {
	var out []Description
	for _, ent := range e.all() {
		msg := ent.describe()
		info, ok := stripped(msg, "key", "device_id", "object_id", "unique_id")
		if !ok {
			continue
		}
		out = append(out, Description{Object: ent.Object(), Kind: kindOf(msg), Info: info})
	}
	return out
}

func (e *Entities) Actions() []Action {
	all := e.allActions()
	out := make([]Action, len(all))
	for i, a := range all {
		out[i] = *a
	}
	return out
}

func (e *Entities) Run(name string, c Call) (any, error) {
	a, ok := e.action((&Action{Name: name}).key())
	if !ok {
		return nil, fmt.Errorf("esphomedevice: no action %q", name)
	}
	return a.Run(c)
}

func (e *Entities) Press(object string) error {
	ent, ok := e.lookup(fnv1(object))
	if !ok {
		return fmt.Errorf("esphomedevice: no entity %q", object)
	}
	b, ok := ent.(*Button)
	if !ok {
		return fmt.Errorf("esphomedevice: %q is %T, not a button", object, ent)
	}
	b.command()
	return nil
}

func (e *Entities) tell(msg proto.Message) {
	keyed, ok := msg.(interface{ GetKey() uint32 })
	if !ok {
		return
	}
	e.mu.RLock()
	ent, found := e.byKey[keyed.GetKey()]
	fns := make([]func(State), 0, len(e.listeners))
	for _, fn := range e.listeners {
		fns = append(fns, fn)
	}
	e.mu.RUnlock()
	if !found || len(fns) == 0 {
		return
	}
	st, ok := stateOf(ent.Object(), msg)
	if !ok {
		return
	}
	for _, fn := range fns {
		fn(st)
	}
}

func stateOf(object string, msg proto.Message) (State, bool) {
	value, ok := stripped(msg, "key", "device_id")
	if !ok {
		return State{}, false
	}
	return State{Object: object, Value: value}, true
}

func stripped(msg proto.Message, drop ...string) (json.RawMessage, bool) {
	if msg == nil {
		return nil, false
	}
	raw, err := stateJSON.Marshal(msg)
	if err != nil {
		return nil, false
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, false
	}
	for _, k := range drop {
		delete(fields, k)
	}
	out, err := json.Marshal(fields)
	if err != nil {
		return nil, false
	}
	return out, true
}

func kindOf(msg proto.Message) string {
	name := string(msg.ProtoReflect().Descriptor().Name())
	name = strings.TrimSuffix(strings.TrimPrefix(name, "ListEntities"), "Response")
	var b strings.Builder
	for i, r := range name {
		if unicode.IsUpper(r) {
			if i > 0 {
				b.WriteByte('_')
			}
			r = unicode.ToLower(r)
		}
		b.WriteRune(r)
	}
	return b.String()
}
