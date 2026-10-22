package control

import (
	"fmt"
	"log/slog"

	"github.com/spf13/cobra"

	harness "github.com/ygelfand/libcountertop/pkg/runtime/control"
	"github.com/ygelfand/libcountertop/pkg/runtime/control/alarmcmd"

	"github.com/ygelfand/echolocal/internal/android/prop"
	"github.com/ygelfand/echolocal/internal/feature/alarm"
	"github.com/ygelfand/echolocal/internal/layout"
	"github.com/ygelfand/echolocal/internal/logging"
)

func (c *Control) tree() *cobra.Command {
	t := harness.NewTree()
	t.Add(harness.DeviceTask, telling()...)
	t.Add(harness.DeviceTask, harness.WaitCommand())
	t.Add(harness.DeviceTask, alarmcmd.Command(alarm.Get))
	t.Add(harness.InputTask, c.inputEngine().Commands()...)
	t.Add(harness.AudioTask, hearing()...)
	t.Add(harness.AudioTask, cancelling()...)
	t.Top(setCommand())
	showing(t)
	for _, more := range boardCommands {
		more(t)
	}
	return t.Root()
}

var boardCommands []func(*harness.Tree)

func telling() []*cobra.Command {
	commands := harness.DeviceCommands(deviceInfo)

	logs := &cobra.Command{
		Use:       "log [debug|info|warn|error]",
		Short:     "Read or change how much goes to logcat, until echod restarts",
		Args:      cobra.MaximumNArgs(1),
		ValidArgs: []string{"debug", "info", "warn", "error"},
		RunE: func(cmd *cobra.Command, args []string) error {
			level, err := harness.LogLevel(&logging.Log.Level, args)
			if err != nil {
				return err
			}
			if len(args) == 1 {
				priority := "I"
				if logging.Log.Level.Level() < slog.LevelInfo {
					priority = "D"
				}
				if err := prop.Set("log.tag."+layout.LogTag, priority); err != nil {
					return err
				}
			}
			fmt.Fprintln(cmd.OutOrStdout(), level)
			return nil
		},
	}

	return append(commands, logs)
}
