package echolocal

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ygelfand/libcountertop/pkg/host/adb"

	"github.com/ygelfand/echolocal/internal/host/profile"
	"github.com/ygelfand/echolocal/internal/update"
)

func resolveEchod(
	ctx context.Context,
	out io.Writer,
	d *adb.Device,
	p profile.Profile,
	manifest, path string,
) ([]byte, string, error) {
	if path != "" {
		data, err := os.ReadFile(path)
		return data, path, err
	}

	if manifest == "" {
		var err error
		if manifest, err = update.Stable.Manifest(ctx); err != nil {
			return nil, "", err
		}
	}
	m, err := update.At(ctx, manifest)
	if err != nil {
		return nil, "", fmt.Errorf("%w\n\nA local binary can be given with --echod", err)
	}

	arch, err := deviceArch(d)
	if err != nil {
		return nil, "", err
	}

	b, err := m.For(p.Board.Codename, arch)
	if err != nil {
		return nil, "", fmt.Errorf("%w\n\nA local binary can be given with --echod", err)
	}

	title := fmt.Sprintf(
		"Fetching echod %s for %s (%.1f MB)",
		m.Version,
		p.Board,
		float64(b.Size)/(1<<20),
	)
	return fetchImage(ctx, out, title, b.Resolve)
}

func deviceArch(d *adb.Device) (string, error) {
	abi, err := d.Getprop("ro.product.cpu.abi")
	if err != nil {
		return "", err
	}

	switch {
	case strings.HasPrefix(abi, "arm64"):
		return "arm64", nil
	case strings.HasPrefix(abi, "armeabi"):
		return "arm", nil
	}
	return "", fmt.Errorf("this device reports abi %q, which echod has no build for", abi)
}
