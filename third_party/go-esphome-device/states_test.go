package esphomedevice

import (
	"encoding/json"
	"testing"
)

func TestListenersHearEveryStateByObjectID(t *testing.T) {
	ents := NewEntities()
	call := &TextSensor{Base: Base{ObjectID: "call_state", Name: "Call"}}
	mute := &Switch{Base: Base{ObjectID: "mic_mute", Name: "Mute"}}
	if err := ents.Add(call, mute); err != nil {
		t.Fatal(err)
	}

	var heard []State
	stop := ents.Listen(func(s State) { heard = append(heard, s) })
	call.Set("ringing")
	mute.Set(true)
	stop()
	call.Set("idle")

	if len(heard) != 2 {
		t.Fatalf("heard %d states, want 2 before cancel and none after", len(heard))
	}
	var text struct {
		State string `json:"state"`
		Key   *int   `json:"key"`
	}
	if err := json.Unmarshal(heard[0].Value, &text); err != nil {
		t.Fatal(err)
	}
	if heard[0].Object != "call_state" || text.State != "ringing" || text.Key != nil {
		t.Errorf("first state %s %s, want call_state ringing without its key", heard[0].Object, heard[0].Value)
	}
	var on struct {
		State bool `json:"state"`
	}
	if err := json.Unmarshal(heard[1].Value, &on); err != nil || heard[1].Object != "mic_mute" || !on.State {
		t.Errorf("second state %s %s, want mic_mute true", heard[1].Object, heard[1].Value)
	}

	states := ents.States()
	if len(states) != 2 || states[0].Object != "call_state" {
		t.Fatalf("snapshot %+v, want both entities in order", states)
	}
	if err := json.Unmarshal(states[0].Value, &text); err != nil || text.State != "idle" {
		t.Errorf("snapshot has call_state %s, want idle", states[0].Value)
	}
}

func TestPressRunsOnlyButtons(t *testing.T) {
	ents := NewEntities()
	pressed := 0
	reboot := &Button{Base: Base{ObjectID: "reboot"}, OnPress: func() { pressed++ }}
	mute := &Switch{Base: Base{ObjectID: "mic_mute"}}
	if err := ents.Add(reboot, mute); err != nil {
		t.Fatal(err)
	}
	if err := ents.Press("reboot"); err != nil || pressed != 1 {
		t.Errorf("Press(reboot) = %v with %d presses, want nil and 1", err, pressed)
	}
	if err := ents.Press("mic_mute"); err == nil {
		t.Error("Press(mic_mute) succeeded on a switch")
	}
	if err := ents.Press("nothing"); err == nil {
		t.Error("Press(nothing) succeeded")
	}
}

func TestActionsListAndRunByName(t *testing.T) {
	ents := NewEntities()
	err := ents.AddActions(&Action{
		Name:    "echo",
		Args:    []Arg{{Name: "word", Type: ArgString}},
		Answers: true,
		Run:     func(c Call) (any, error) { return map[string]string{"said": c.String("word")}, nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	listed := ents.Actions()
	if len(listed) != 1 || listed[0].Name != "echo" || len(listed[0].Args) != 1 || !listed[0].Answers {
		t.Fatalf("listed %+v, want echo with one argument that answers", listed)
	}
	c, err := NewCall(map[string]any{"word": "hi"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := ents.Run("echo", c)
	if said, _ := got.(map[string]string); err != nil || said["said"] != "hi" {
		t.Errorf("Run(echo) = %v, %v; want said hi", got, err)
	}
	if _, err := ents.Run("nope", c); err == nil {
		t.Error("Run(nope) succeeded")
	}
}

func TestNewCallReadsBackEveryType(t *testing.T) {
	c, err := NewCall(map[string]any{"who": "office", "video": true, "count": float64(3), "level": 0.5})
	if err != nil {
		t.Fatal(err)
	}
	if c.String("who") != "office" || !c.Bool("video") || c.Int("count") != 3 || c.Float("count") != 3 ||
		c.Float("level") != 0.5 {
		t.Errorf("read back %q %v %d %v %v", c.String("who"), c.Bool("video"), c.Int("count"),
			c.Float("count"), c.Float("level"))
	}
	if _, err := NewCall(map[string]any{"list": []any{1}}); err == nil {
		t.Error("NewCall took a list")
	}
}

func TestDescriptionsNameKindAndUnit(t *testing.T) {
	ents := NewEntities()
	temp := &Sensor{
		Base: Base{ObjectID: "cpu_temp", Name: "CPU", Category: CategoryDiagnostic},
		Unit: "°C",
	}
	call := &TextSensor{Base: Base{ObjectID: "call_state", Name: "Call"}}
	if err := ents.Add(temp, call); err != nil {
		t.Fatal(err)
	}

	got := ents.Descriptions()
	if len(got) != 2 {
		t.Fatalf("%d descriptions, want 2", len(got))
	}
	var info struct {
		Name     string  `json:"name"`
		Unit     string  `json:"unit_of_measurement"`
		Category string  `json:"entity_category"`
		Key      *int    `json:"key"`
		ObjectID *string `json:"object_id"`
	}
	if err := json.Unmarshal(got[0].Info, &info); err != nil {
		t.Fatal(err)
	}
	if got[0].Object != "cpu_temp" || got[0].Kind != "sensor" || info.Name != "CPU" ||
		info.Unit != "°C" || info.Category != "ENTITY_CATEGORY_DIAGNOSTIC" ||
		info.Key != nil || info.ObjectID != nil {
		t.Errorf("first %s %s %s, want cpu_temp sensor CPU in °C, diagnostic, no key or object id",
			got[0].Object, got[0].Kind, got[0].Info)
	}
	if got[1].Kind != "text_sensor" {
		t.Errorf("second kind %q, want text_sensor", got[1].Kind)
	}
}
