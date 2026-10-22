package board

import "strings"

var All = []Board{Biscuit, Crown, Checkers, Cronos, Athena, Donut, Rook, Radar}

func For(device string) (Board, bool) {
	device = strings.TrimSpace(device)
	if device == "" {
		return Board{}, false
	}
	for _, b := range All {
		if b.Device == device || (b.Device != "" && b.Codename == device) {
			return b, true
		}
	}
	return Board{}, false
}

func ByCodename(name string) (Board, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Board{}, false
	}
	for _, b := range All {
		if b.Codename == name {
			return b, true
		}
	}
	return Board{}, false
}

func Intended() []string {
	var out []string
	for _, b := range All {
		if b.Device == "" {
			out = append(out, b.Codename)
		}
	}
	return out
}
