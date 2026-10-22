//go:build !board_biscuit

package control

import (
	"fmt"

	"github.com/spf13/cobra"
	harness "github.com/ygelfand/libcountertop/pkg/runtime/control"
	"github.com/ygelfand/libcountertop/pkg/runtime/control/screencmd"

	"github.com/ygelfand/echolocal/internal/feature/nws"
	"github.com/ygelfand/echolocal/internal/feature/timer"
	"github.com/ygelfand/echolocal/internal/hardware/display"
)

func showing(t *harness.Tree) {
	shot := &cobra.Command{
		Use:   "shot <path>",
		Short: "Write what the panel is showing to a PNG",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := screencmd.Shot(display.Get(), args[0]); err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), args[0])
			return nil
		},
	}

	open, stack, ready := panelCommands()
	t.Add(
		harness.DisplayTask,
		shot,
		thumbsCommand(),
		open,
		stack,
		screencmd.Timer(timer.Get),
		screencmd.Warning(nws.Get),
	)
	t.Add(harness.DeviceTask, ready)
}
