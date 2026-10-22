package echod

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/ygelfand/echolocal/internal/layout"
)

func newRoot() *cobra.Command {
	root := &cobra.Command{
		Use:   "echod",
		Short: "EchoLocal device agent",
		Long: "echod runs on an Echo Dot or Echo Show and presents it to Home Assistant as an ESPHome\n" +
			"voice satellite. The tools subtree exposes the same hardware access for\n" +
			"diagnostics.",
		SilenceUsage: true,
		Version:      layout.VersionString(),
	}

	root.AddCommand(newRunCmd())
	root.AddCommand(newToolsCmd())
	root.AddCommand(newCtlCmd())
	return root
}

func Execute() {
	// init starts echod with no arguments.
	if len(os.Args) == 1 {
		os.Args = append(os.Args, "run")
	}
	if err := newRoot().Execute(); err != nil {
		os.Exit(1)
	}
}
