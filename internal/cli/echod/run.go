package echod

import (
	"log/slog"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/ygelfand/echolocal/internal/boot"
	"github.com/ygelfand/echolocal/internal/logging"
)

func newRunCmd() *cobra.Command {
	c := &cobra.Command{
		Use:   "run",
		Short: "Run the device agent",
		Long: "Plays the boot animation, then stays resident. init starts echod and restarts it\n" +
			"if it exits. On the Echo Dot it runs in place of Amazon's ledcontroller service,\n" +
			"so nothing else drives the ring.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			h := logging.Log.Open(cmd.ErrOrStderr())
			defer h.Close()
			slog.SetDefault(slog.New(h))

			// init discards stderr.
			defer func() {
				if r := recover(); r != nil {
					slog.Error("panic", "panic", r, "stack", string(debug.Stack()))
					_ = h.Close()
					panic(r)
				}
			}()

			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()

			signal.Ignore(syscall.SIGHUP)

			return boot.Run(ctx)
		},
	}

	return c
}
