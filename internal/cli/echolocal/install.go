package echolocal

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"

	"charm.land/bubbles/v2/spinner"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/spf13/cobra"
	"github.com/ygelfand/libcountertop/pkg/host/adb"
	"github.com/ygelfand/libcountertop/pkg/host/prompt"
	"github.com/ygelfand/libcountertop/pkg/host/wifisetup"

	"github.com/ygelfand/echolocal/internal/android/wifi"
	"github.com/ygelfand/echolocal/internal/host/installer"
)

var (
	styleDone   = lipgloss.NewStyle().Foreground(lipgloss.Color("42"))
	styleSkip   = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	styleFail   = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	styleActive = lipgloss.NewStyle().Foreground(lipgloss.Color("39"))
	styleDetail = lipgloss.NewStyle().Foreground(lipgloss.Color("244"))
	styleCount  = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	styleTitle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))

	styleKeyBox = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("42")).
			Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("42")).
			Padding(0, 2).MarginLeft(2)
)

func newInstallCmd() *cobra.Command {
	var (
		serial    string
		echod     string
		manifest  string
		name      string
		bootImage string
		ssid      string
		password  string
		wps       bool
		genKey    bool
		flashOnly bool
		reflash   bool
		assumeYes bool
		doReboot  bool
		noReboot  bool
	)

	c := &cobra.Command{
		Use:   "install",
		Short: "Install echod on a connected Echo Dot or Echo Show",
		Long: "Installs echod into /system/app/echod as an init service, in place of Amazon's\n" +
			"ledcontroller on the Echo Dot, so init starts and supervises it. Safe to re-run.\n\n" +
			"Begins by checking that the device has root, without which there is no root adbd and\n" +
			"nothing below it can run. A device that has it is left alone; one that does not has\n" +
			"EchoLocal's boot image written from recovery, which needs TWRP as the recovery\n" +
			"partition.",
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			out := cmd.OutOrStdout()

			d, err := attach(cmd.Context(), out, serial)
			if err != nil {
				return err
			}

			p, err := profileFor(d)
			if err != nil {
				return err
			}

			cfg := installer.Config{ZeroPSK: !genKey, Profile: p, Reflash: reflash}

			state, err := installer.Probe(d)
			if err != nil {
				return err
			}
			if (!state.Ready || reflash) && (p.Boot.SHA256 != "" || !p.System.Empty()) {
				if p.Boot.SHA256 != "" {
					if cfg.BootImage, cfg.BootImageFrom, err = resolveBootImage(
						cmd.Context(),
						out,
						p,
						bootImage,
					); err != nil {
						return err
					}
				}
				if cfg.Approved, err = approveFlash(
					cmd.Context(),
					out,
					d,
					state,
					cfg.BootImageFrom,
					assumeYes,
				); err != nil {
					return err
				}
			}

			if err := render(cmd.Context(), out, "Verifying device boot status",
				"✓ device has root",
				func(report installer.Reporter) error {
					return installer.FlashBoot(cmd.Context(), d, cfg, report)
				}); err != nil {
				return err
			}
			if flashOnly {
				return nil
			}

			if cfg.Echod, _, err = resolveEchod(
				cmd.Context(),
				out,
				d,
				p,
				manifest,
				echod,
			); err != nil {
				return err
			}
			chosen, err := resolveName(cmd.Context(), out, d, name)
			if err != nil {
				return err
			}
			cfg.Name = chosen

			var settles bool
			if err := render(cmd.Context(), out, "Installing EchoLocal",
				"✓ echod installed and running",
				func(report installer.Reporter) error {
					var err error
					settles, err = installer.Install(cmd.Context(), d, cfg, report)
					return err
				}); err != nil {
				return err
			}

			if err := wifisetup.Ensure(
				cmd.Context(),
				out,
				wifi.On(d),
				wifisetup.Request{SSID: ssid, Password: password, WPS: wps},
			); err != nil {
				if !errors.Is(err, prompt.ErrCanceled) {
					return err
				}
				fmt.Fprintf(
					out,
					"%s\n",
					styleSkip.Render(
						"• wireless left unconfigured; run `echolocal wifi` when ready",
					),
				)
			}

			if err := offerReboot(
				cmd.Context(),
				out,
				d,
				settles,
				rebootChoiceOf(doReboot, noReboot),
			); err != nil {
				return err
			}
			return printPairing(out, d, chosen)
		},
	}

	c.Flags().StringVar(&serial, "serial", "", "device serial, when more than one is connected")
	c.Flags().StringVar(&echod, "echod", "", "echod binary to install, instead of fetching one")
	c.Flags().StringVar(&manifest, "manifest", "",
		"manifest to resolve echod from, as a url or a local file, instead of the release channel")
	c.Flags().
		StringVar(&bootImage, "boot-image", "", "boot image to write, instead of the one shipped")
	c.Flags().StringVar(&ssid, "ssid", "", "network to join, instead of picking from a scan")
	c.Flags().StringVar(&password, "password", "", "passphrase, for an --ssid that needs one")
	c.Flags().BoolVar(&wps, "wps", false, "join by pressing the router's WPS button instead")
	c.Flags().BoolVar(&flashOnly, "flash-only", false,
		"write the boot image and stop, without installing echod")
	c.Flags().
		BoolVar(&reflash, "reflash", false, "write the boot image even when the device already has root")
	c.Flags().BoolVarP(&assumeYes, "yes", "y", false,
		"do not ask before overwriting the boot partition")
	c.Flags().BoolVar(&genKey, "generate-key", false,
		"generate the encryption key here, to paste in by hand, instead of letting Home Assistant push one")
	c.Flags().BoolVar(&doReboot, "reboot", false, "reboot at the end without asking")
	c.Flags().BoolVar(&noReboot, "no-reboot", false, "finish without rebooting, and without asking")
	c.MarkFlagsMutuallyExclusive("reboot", "no-reboot")
	nameFlag(c, &name)
	return c
}

func printPairing(out io.Writer, d *adb.Device, name string) error {
	key, err := installer.ReadKey(d)
	if err != nil {
		return err
	}

	if key == "" {
		fmt.Fprintf(out, "\n%s\n%s\n",
			styleTitle.Render("Add to Home Assistant"),
			styleDetail.Render("  Settings → Devices → ESPHome → "+name+
				"\n  zero psk provisioning enabled"))
		return nil
	}

	fmt.Fprintf(out, "\n%s\n%s\n",
		styleTitle.Render("Add to Home Assistant"),
		styleDetail.Render("  Settings → Devices → ESPHome → "+name+", then paste this key:"))
	fmt.Fprintln(out, styleKeyBox.Render(key))
	return nil
}

func render(
	ctx context.Context,
	out io.Writer,
	title, success string,
	run func(installer.Reporter) error,
) error {
	if !prompt.IsTerminal() {
		return run(func(e installer.Event) {
			if e.Status == installer.Running {
				return
			}
			fmt.Fprintf(
				out,
				"[%d/%d] %s %s %s\n",
				e.Step,
				e.Total,
				statusWord(e.Status),
				e.Name,
				detailOf(e),
			)
		})
	}

	m := &installModel{
		title:   title,
		success: success,
		spin:    spinner.New(spinner.WithSpinner(spinner.Dot)),
	}
	m.spin.Style = styleActive

	p := tea.NewProgram(m, tea.WithContext(ctx), tea.WithOutput(out))
	go func() { p.Send(doneMsg{err: run(func(e installer.Event) { p.Send(e) })}) }()

	final, err := p.Run()
	if err != nil {
		return err
	}
	return final.(*installModel).err
}

type doneMsg struct{ err error }

type installModel struct {
	title    string
	success  string
	spin     spinner.Model
	events   []installer.Event
	active   *installer.Event
	err      error
	finished bool
}

func (m *installModel) Init() tea.Cmd { return m.spin.Tick }

func (m *installModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case installer.Event:
		if msg.Status == installer.Running {
			e := msg
			m.active = &e
		} else {
			m.active = nil
			m.events = append(m.events, msg)
		}
		return m, nil

	case doneMsg:
		m.err, m.finished, m.active = msg.err, true, nil
		return m, tea.Quit

	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			m.err, m.finished = prompt.ErrCanceled, true
			return m, tea.Quit
		}
		return m, nil

	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m *installModel) View() tea.View {
	var b strings.Builder
	b.WriteString(styleTitle.Render(m.title) + "\n\n")

	for _, e := range m.events {
		fmt.Fprintf(&b, "  %s %s %s\n", mark(e.Status), e.Name, detailOf(e))
	}
	if m.active != nil {
		fmt.Fprintf(&b, "  %s %s %s\n", m.spin.View(), m.active.Name,
			styleCount.Render(fmt.Sprintf("(%d/%d)", m.active.Step, m.active.Total)))
	}

	if m.finished {
		if m.err != nil {
			b.WriteString("\n" + styleFail.Render("✗ failed: "+m.err.Error()) + "\n")
		} else {
			b.WriteString("\n" + styleDone.Render(m.success) + "\n")
		}
	}
	return tea.NewView(b.String())
}

func mark(st installer.Status) string {
	switch st {
	case installer.Done:
		return styleDone.Render("✓")
	case installer.Skipped:
		return styleSkip.Render("•")
	case installer.Failed:
		return styleFail.Render("✗")
	}
	return " "
}

func statusWord(st installer.Status) string {
	switch st {
	case installer.Done:
		return "ok"
	case installer.Skipped:
		return "skip"
	case installer.Failed:
		return "FAIL"
	}
	return ""
}

func detailOf(e installer.Event) string {
	if e.Err != nil {
		return styleFail.Render(e.Err.Error())
	}
	if e.Detail == "" {
		return ""
	}
	return styleDetail.Render(e.Detail)
}
