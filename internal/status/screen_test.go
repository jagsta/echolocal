package status

import (
	"testing"

	"github.com/ygelfand/libcountertop/pkg/settings/schema"
)

func TestTheScreenGroupShowsTheBacklight(t *testing.T) {
	s := schema.DefaultScreen()
	s.Backlight = 42

	g := Screen(s)
	if g.Title != "Screen" || len(g.Rows) != 5 {
		t.Fatalf("group %+v", g)
	}
	for _, r := range g.Rows {
		if r.Name == "Backlight" && r.Value != "42%" {
			t.Errorf("backlight shown as %q", r.Value)
		}
	}
}
