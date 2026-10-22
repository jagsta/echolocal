package update

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"runtime"
	"time"

	"github.com/ygelfand/libcountertop/pkg/network/releases"
)

// Home Assistant ranks Version with AwesomeVersion; an unrankable one pins the update card on.
type Manifest struct {
	Version string `json:"version"`

	URL      string            `json:"url"`
	SHA256   string            `json:"sha256"`
	Size     int64             `json:"size"`
	Binaries map[string]Binary `json:"binaries"`

	Boards map[string]map[string]Binary `json:"boards,omitempty"`

	Title      string `json:"title,omitempty"`
	Notes      string `json:"notes,omitempty"`
	ReleaseURL string `json:"release_url,omitempty"`
}

type Binary struct {
	URL    string `json:"url"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

const flatArch = "arm64"

var (
	arch     = runtime.GOARCH
	codename string
)

func Use(board string) { codename = board }

const manifestTimeout = 10 * time.Second

const maxManifest = 64 << 10

func Fetch(ctx context.Context, c Channel) (Manifest, error) {
	var m Manifest

	ctx, cancel := context.WithTimeout(ctx, manifestTimeout)
	defer cancel()

	url, err := c.Manifest(ctx)
	if err != nil {
		return m, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return m, err
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return m, fmt.Errorf("update: fetching %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return m, fmt.Errorf("update: fetching %s: %s", url, resp.Status)
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxManifest)).Decode(&m); err != nil {
		return m, fmt.Errorf("update: reading the manifest at %s: %w", url, err)
	}
	return m, m.Valid()
}

func FetchTag(ctx context.Context, tag string) (Manifest, error) {
	return At(ctx, releases.Manifest(repo, tag))
}

func Releases(ctx context.Context) ([]releases.Release, error) {
	all, err := releases.List(ctx, repo)
	if err != nil {
		return nil, err
	}
	var fits []releases.Release
	for _, r := range all {
		m, err := FetchTag(ctx, r.Tag)
		if err != nil {
			slog.Debug("skipping a release", "version", r.Tag, "err", err)
			continue
		}
		if _, err := m.For(codename, arch); err != nil {
			slog.Debug("skipping a release", "version", r.Tag, "err", err)
			continue
		}
		fits = append(fits, r)
	}
	return fits, nil
}

func (m Manifest) flat() Binary {
	return Binary{URL: m.URL, SHA256: m.SHA256, Size: m.Size}
}

func (m Manifest) Valid() error {
	if m.Version == "" {
		return errors.New("update: the manifest names no version")
	}

	if flat := m.flat(); flat != (Binary{}) {
		if err := flat.valid(m.Version, flatArch); err != nil {
			return err
		}
	} else if len(m.Binaries) == 0 {
		return fmt.Errorf("update: the manifest for %s carries no binaries", m.Version)
	}

	for a, b := range m.Binaries {
		if err := b.valid(m.Version, a); err != nil {
			return err
		}
	}
	for board, builds := range m.Boards {
		for a, b := range builds {
			if err := b.valid(m.Version, board+" "+a); err != nil {
				return err
			}
		}
	}
	return nil
}

func (b Binary) valid(version, arch string) error {
	switch {
	case b.URL == "":
		return fmt.Errorf("update: the %s binary for %s has no url", arch, version)
	case len(b.SHA256) != 64:
		return fmt.Errorf("update: the %s binary for %s has no usable sha256", arch, version)
	case b.Size <= 0:
		return fmt.Errorf("update: the %s binary for %s gives no size", arch, version)
	}
	return nil
}

func (m Manifest) For(board, arch string) (Binary, error) {
	if b, ok := m.Boards[board][arch]; ok {
		return b, nil
	}
	if board == "" {
		return Binary{}, fmt.Errorf(
			"update: %s cannot be installed without knowing which board this is",
			m.Version,
		)
	}
	return Binary{}, fmt.Errorf("update: %s carries no %s build for %s", m.Version, arch, board)
}
