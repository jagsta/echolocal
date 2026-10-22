// Package labels carries the fork-domains' UI text (diag, intercom): upstream
// moved user-facing strings into libcountertop/pkg/say, whose catalogue is
// module-owned and can't carry fork domains, but upstream's setting/labels
// test now fails on raw keys unless the table's Messages translate them.
package labels

import (
	_ "embed"

	"github.com/ygelfand/libcountertop/pkg/say"
	setting "github.com/ygelfand/libcountertop/pkg/settings"
)

//go:embed en.yaml
var en []byte

var lines = map[string]string{}

func init() {
	// flat "key: text" yaml: keys are dotted identifiers without spaces, so a
	// line-based parse keeps this dependency-free.
	for _, l := range splitLines(en) {
		for i := 0; i < len(l); i++ {
			if l[i] == ':' {
				lines[trim(l[:i])] = trim(l[i+1:])
				break
			}
		}
	}
}

func splitLines(b []byte) []string {
	var out []string
	start := 0
	for i, c := range b {
		if c == '\n' {
			if line := b[start:i]; len(line) > 0 {
				out = append(out, string(line))
			}
			start = i + 1
		}
	}
	if start < len(b) {
		out = append(out, string(b[start:]))
	}
	return out
}

func trim(s string) string {
	for len(s) > 0 && (s[0] == ' ' || s[0] == '\t' || s[0] == '"') {
		s = s[1:]
	}
	for len(s) > 0 && (s[len(s)-1] == ' ' || s[len(s)-1] == '\t' || s[len(s)-1] == '\r' || s[len(s)-1] == '"') {
		s = s[:len(s)-1]
	}
	return s
}

// Has reports whether the fork catalogue carries id.
func Has(id string) bool {
	_, ok := lines[id]
	return ok
}

// Text returns the fork label for id when present, id itself otherwise
// (the caller falls through to say.T, which returns the raw key for
// untranslated identifiers).
func Text(id string) string {
	if v, ok := lines[id]; ok {
		return v
	}
	return id
}

// Messages wires the fork labels into a settings table: fork keys resolve
// from en.yaml above, everything else falls through to upstream's say
// catalogue.
func Messages() setting.Messages {
	return setting.Messages{
		Text: func(id string) string {
			if v, ok := lines[id]; ok {
				return v
			}
			return say.T(id)
		},
		Missing: func(id string) bool {
			return !Has(id) && say.Missing(id)
		},
	}
}
