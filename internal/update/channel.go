package update

import (
	"context"

	"github.com/ygelfand/libcountertop/pkg/network/releases"
)

type Channel int

const (
	// GitHub's "latest" excludes prereleases.
	Stable Channel = iota

	Dev
)

const repo = "https://github.com/ygelfand/echolocal"

func (c Channel) Label() string {
	switch c {
	case Dev:
		return "dev"
	}
	return "stable"
}

// The GitHub REST API allows 60 unauthenticated requests an hour; asset downloads do not count.
func (c Channel) Manifest(ctx context.Context) (string, error) {
	if c != Dev {
		return repo + "/releases/latest/download/manifest.json", nil
	}
	tag, err := releases.Newest(ctx, repo)
	if err != nil {
		return "", err
	}
	return releases.Manifest(repo, tag), nil
}

func Channels() []Channel { return []Channel{Stable, Dev} }
