package echolocal

import (
	"github.com/spf13/cobra"
	"github.com/ygelfand/libcountertop/pkg/host/wifisetup"

	"github.com/ygelfand/echolocal/internal/android/wifi"
)

func newWifiCmd() *cobra.Command {
	var (
		serial string
		r      wifisetup.Request
	)

	c := &cobra.Command{
		Use:   "wifi",
		Short: "Connect the device to a wireless network",
		Long: "Scans, joins and reports. The device needs this before Home Assistant can reach it,\n" +
			"and it needs root first, which `install --flash-only` provides.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, out := cmd.Context(), cmd.OutOrStdout()

			d, err := connect(ctx, out, serial)
			if err != nil {
				return err
			}
			return wifisetup.Ensure(ctx, out, wifi.On(d), r)
		},
	}

	c.Flags().StringVar(&serial, "serial", "", "device serial, when more than one is connected")
	c.Flags().StringVar(&r.SSID, "ssid", "", "network to join, instead of picking from a scan")
	c.Flags().StringVar(&r.Password, "password", "", "passphrase, for an --ssid that needs one")
	c.Flags().BoolVar(&r.WPS, "wps", false, "join by pressing the router's WPS button instead")
	c.Flags().BoolVar(&r.Force, "force", false, "pick a network even when already connected")
	return c
}
