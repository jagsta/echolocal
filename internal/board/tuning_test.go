package board

import (
	"os"
	"strings"
	"testing"

	"github.com/ygelfand/echolocal/internal/lib/asp"
)

func TestTheVendorTuningLoadsOnEveryBoardThatListsIt(t *testing.T) {
	for _, b := range All {
		if !b.Tuning.Listed() {
			continue
		}
		t.Run(b.Codename, func(t *testing.T) {
			dir := os.Getenv("ECHOLOCAL_VENDOR_" + strings.ToUpper(b.Codename))
			if dir == "" {
				t.Skipf(
					"set ECHOLOCAL_VENDOR_%s to a copy of %s",
					strings.ToUpper(b.Codename),
					asp.VendorDir,
				)
			}
			tuning, err := asp.Load(dir, b.Tuning)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if _, err := tuning.Chain(384); err != nil {
				t.Fatalf("Chain: %v", err)
			}
		})
	}
}
