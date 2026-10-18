package control

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ygelfand/echolocal/internal/hardware/mic"
)

func cancelling() []*cobra.Command {
	cancel := &cobra.Command{
		Use:   "cancel",
		Short: "The echo canceller: whether it is running and learning, and how much it removes",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			s := mic.Get()
			fmt.Fprintf(cmd.OutOrStdout(), "running  %t\nlearning %t\nfrozen   %t\nerle_db  %.1f\ndropped  %d\n",
				s.Cancelling(), s.Adapting(), s.Frozen(), s.ERLE(), s.Dropped())
			return nil
		},
	}

	adapt := &cobra.Command{
		Use:   "adapt on|off",
		Short: "Let the canceller learn, or hold it at what it knows",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			switch args[0] {
			case "on":
				mic.Get().Freeze(false)
			case "off":
				mic.Get().Freeze(true)
			default:
				return fmt.Errorf("%q is not on or off", args[0])
			}
			return nil
		},
	}

	reset := &cobra.Command{
		Use:   "reset",
		Short: "Forget what the canceller learned",
		Args:  cobra.NoArgs,
		RunE: func(_ *cobra.Command, _ []string) error {
			mic.Get().ResetCancel()
			return nil
		},
	}

	cancel.AddCommand(adapt, reset)
	return []*cobra.Command{cancel}
}
