package all

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/ygelfand/libcountertop/pkg/runtime/webapi"

	"github.com/ygelfand/echolocal/internal/component"
	"github.com/ygelfand/echolocal/internal/config"
)

func TestEverySettingHasItsLabels(t *testing.T) {
	config.Use(filepath.Join(t.TempDir(), "state.json"))
	component.Default().Entities()
	if missing := component.Settings.Unlabeled(); len(missing) > 0 {
		t.Errorf("en.yaml has no text for:\n\t%s", strings.Join(missing, "\n\t"))
	}
	if loose := webapi.Unplaced(&component.Settings); len(loose) > 0 {
		// webapi.Sections is libcountertop-owned; the fork's domains (diag, intercom)
		// can't be added there. They fall into the system section at runtime, which
		// is a valid placement — so only flag domains that are neither placed nor
		// fork-only.
		forkOnly := map[string]bool{"diag": true, "intercom": true}
		var real []string
		for _, d := range loose {
			if !forkOnly[d] {
				real = append(real, d)
			}
		}
		if len(real) > 0 {
			t.Errorf("webapi sections hold no place for:\n	%s", strings.Join(real, "\n	"))
		}
	}
}
