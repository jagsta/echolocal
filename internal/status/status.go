package status

import (
	_ "embed"
	"fmt"
	"html/template"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	netaddress "github.com/ygelfand/libcountertop/pkg/network/address"
	"github.com/ygelfand/libcountertop/pkg/runtime/startup"
	"github.com/ygelfand/libcountertop/pkg/settings/schema"
	"github.com/ygelfand/libcountertop/pkg/system/metrics"
)

//go:embed status.html
var statusHTML string

var statusPage = template.Must(template.New("status").Parse(statusHTML))

type Page struct {
	Name    string
	Model   string
	Version string

	Adopted bool

	Parts  []Part
	Groups []Group
}

type Part struct {
	Name string

	State string

	Doing string
}

type Group struct {
	Title string
	Rows  []Row
}

type Row struct{ Name, Value string }

type Thermal struct{ Name, Zone string }

const Unknown = "—"

func Handler(page func() Page) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")

		if err := statusPage.Execute(w, page()); err != nil {
			slog.Error("rendering the status page failed", "err", err)
		}
	}
}

func Parts(progress []startup.Progress) []Part {
	out := make([]Part, 0, len(progress))
	for _, p := range progress {
		out = append(out, Part{Name: p.Name, State: state(p), Doing: p.Doing})
	}
	return out
}

func state(p startup.Progress) string {
	switch {
	case p.Failed:
		return "failed"
	case p.Done:
		return "ready"
	}
	return "waiting"
}

func Network(wifi, mac string, r metrics.Reader) Group {
	rows := []Row{
		{"Wi-Fi network", or(wifi)},
		{"MAC", or(mac)},
	}

	ips := netaddress.Addresses()
	if len(ips) == 0 {
		rows = append(rows, Row{"Address", Unknown})
	}
	for i, ip := range ips {
		name := "Address"
		if i > 0 {
			name = ""
		}
		rows = append(rows, Row{name, ip.String()})
	}

	if signal, _, _ := r.Wifi(); signal.Known {
		rows = append(rows, Row{"Signal", fmt.Sprintf("%.0f dBm", signal.Value)})
	}
	return Group{Title: "Network", Rows: rows}
}

func Clock(chosen, home string) Group {
	now := time.Now()

	zone, from := chosen, "Set on the device"
	if zone == "" {
		zone, from = home, "From Home Assistant"
	}
	if zone == "" {
		zone, from = Unknown, "Nothing has said"
	}

	name, offset := now.Zone()
	return Group{Title: "Time", Rows: []Row{
		{"Local time", now.Format("15:04:05")},
		{"Date", now.Format("Monday, 2 January 2006")},
		{"Zone", zone},
		{"Source", from},
		{"Running as", fmt.Sprintf("%s (%s)", name, utc(offset))},
	}}
}

func Screen(s schema.Screen) Group {
	return Group{Title: "Screen", Rows: []Row{
		{Name: "Theme", Value: s.Theme},
		{Name: "Brightness", Value: s.Mode.Label()},
		{Name: "Backlight", Value: Percent(s.Backlight)},
		{Name: "Clock", Value: s.Hours.Label()},
		{Name: "Drawer", Value: s.Drawer.Label()},
	}}
}

func utc(seconds int) string {
	sign := "+"
	if seconds < 0 {
		sign, seconds = "-", -seconds
	}
	return fmt.Sprintf("UTC%s%02d:%02d", sign, seconds/3600, seconds%3600/60)
}

func System(r metrics.Reader, stateDir string, thermal []Thermal) Group {
	rows := []Row{{"Uptime", since(r.Uptime().Value)}}

	if busy, total := r.CPU(); busy.Known && total.Known && total.Value > 0 {
		rows = append(
			rows,
			Row{"CPU since boot", fmt.Sprintf("%.1f%% busy", busy.Value/total.Value*100)},
		)
	}

	if one, five := r.Load(); one.Known {
		rows = append(
			rows,
			Row{"Load", fmt.Sprintf("%.2f, %.2f (floor ~3)", one.Value, five.Value)},
		)
	}
	if _, online := r.Cores(); online.Known {
		rows = append(rows, Row{"Cores online", strconv.Itoa(int(online.Value))})
	}
	// /proc/meminfo reports kB.
	if free, total := r.Memory(); free.Known && total.Known {
		rows = append(
			rows,
			Row{
				"Memory free",
				fmt.Sprintf("%s of %s", bytes(free.Value*1024), bytes(total.Value*1024)),
			},
		)
	}
	if free, err := metrics.Free(stateDir); err == nil {
		rows = append(rows, Row{"Disk free", bytes(float64(free))})
	}
	return Group{Title: "System", Rows: append(rows, temperatures(r, thermal)...)}
}

func temperatures(r metrics.Reader, thermal []Thermal) []Row {
	zones := r.Temperatures()

	var out []Row
	for _, z := range thermal {
		if v, ok := zones[z.Zone]; ok {
			out = append(out, Row{z.Name, fmt.Sprintf("%.1f °C", v)})
		}
	}
	return out
}

func since(seconds float64) string {
	d := time.Duration(seconds) * time.Second

	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd %dh", int(d.Hours())/24, int(d.Hours())%24)
	case d >= time.Hour:
		return fmt.Sprintf("%dh %dm", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%dm %ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%ds", int(d.Seconds()))
}

func bytes(v float64) string {
	units := []string{"B", "KiB", "MiB", "GiB"}

	i := 0
	for v >= 1024 && i < len(units)-1 {
		v, i = v/1024, i+1
	}
	if i == 0 {
		return fmt.Sprintf("%.0f %s", v, units[i])
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}

func Percent(v int) string { return strconv.Itoa(v) + "%" }

func or(s string) string {
	if strings.TrimSpace(s) == "" {
		return Unknown
	}
	return s
}
