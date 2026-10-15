package diag

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	esphome "github.com/ygelfand/go-esphome-device"

	"github.com/ygelfand/libcountertop/pkg/audio/upsample"
	sharedwake "github.com/ygelfand/libcountertop/pkg/inference/wake"
	netaddress "github.com/ygelfand/libcountertop/pkg/network/address"
	"github.com/ygelfand/libcountertop/pkg/runtime/collector"
	sharedcomponent "github.com/ygelfand/libcountertop/pkg/runtime/component"
	"github.com/ygelfand/libcountertop/pkg/system/metrics"

	"github.com/ygelfand/echolocal/internal/android/firewall"
	"github.com/ygelfand/echolocal/internal/android/prop"
	"github.com/ygelfand/echolocal/internal/android/setup"
	"github.com/ygelfand/echolocal/internal/component"
	"github.com/ygelfand/echolocal/internal/config"
	"github.com/ygelfand/echolocal/internal/feature/bluetooth"
	"github.com/ygelfand/echolocal/internal/feature/wakeword"
	"github.com/ygelfand/echolocal/internal/hardware/mic"
	"github.com/ygelfand/echolocal/internal/hardware/speaker"
	"github.com/ygelfand/echolocal/internal/layout"
	"github.com/ygelfand/echolocal/internal/lib/wake"
)

func init() {
	component.Register(sharedcomponent.Network, Get, sharedcomponent.Order(90))
}

var Ambient func() metrics.Reading

// mtktscpu is the CPU, mtktswmt the wifi and Bluetooth combo chip.
const (
	cpuZone   = "mtktscpu"
	radioZone = "mtktswmt"
)

// The boot image sets service.adb.tcp.port to this.
const adbPort = 5555

type Diag struct {
	testPlayback *esphome.Button

	cache *sharedwake.CacheEntities
	free  *esphome.Sensor

	temperature *esphome.Sensor
	radioTemp   *esphome.Sensor
	cores       *esphome.Sensor
	coresOnline *esphome.Sensor
	load        *esphome.Sensor
	memory      *esphome.Sensor
	lux         *esphome.Sensor
	roomLevel   *esphome.Sensor
	roomFloor   *esphome.Sensor

	luxPath string

	adb   *esphome.Switch
	ip    *esphome.TextSensor
	color *esphome.TextSensor

	boardName *esphome.TextSensor

	signal *esphome.Sensor
	rxRate *esphome.Sensor
	txRate *esphome.Sensor
	ads    *esphome.Sensor

	interval *esphome.Number
	minCores *esphome.Number

	wake chan struct{}

	last struct {
		at       time.Time
		rx, tx   float64
		reports  uint64
		recorded bool
	}
}

var (
	once   sync.Once
	shared *Diag
)

func Get() *Diag {
	once.Do(func() {
		shared = &Diag{wake: make(chan struct{}, 1)}
		shared.storage()
		shared.hardware()
		shared.radio()
		shared.remote()
		shared.playback()
		shared.identity()
		shared.collector()
		settingsOnce.Do(func() { SettingsShared() })

		component.Subscribed.Listen(func(struct{}) { shared.soon() })
	})
	return shared
}

func (d *Diag) Name() string { return "diagnostics" }

func (d *Diag) Entities() []esphome.Entity {
	return append(d.cache.Entities(), []esphome.Entity{
		d.free,
		d.temperature, d.radioTemp, d.cores, d.coresOnline, d.load, d.memory, d.lux,
		d.roomLevel, d.roomFloor,
		d.adb, d.ip, d.color, d.boardName, d.signal, d.rxRate, d.txRate, d.ads,
		d.testPlayback, d.interval, d.minCores,
	}...)
}

func (d *Diag) collector() {
	d.interval = &esphome.Number{
		Base: esphome.Base{
			ObjectID: "metrics_interval",
			Name:     "Metrics interval",
			Icon:     "mdi:timer-sync",
			Category: esphome.CategoryConfig,
		},
		Min: 10, Max: 3600, Step: 10, Unit: "s",
		Mode: esphome.NumberBox,
	}

	d.interval.OnCommand = func(v float32) {
		if err := config.Set().Diag().Interval(int(v)); err != nil {
			slog.Error("saving the metrics interval failed", "err", err)
		}

		d.interval.Set(float32(config.Get().Diag.Interval))

		d.soon()
	}

	d.minCores = &esphome.Number{
		Base: esphome.Base{
			ObjectID: "min_cores",
			Name:     "Minimum CPU cores",
			Icon:     "mdi:cpu-64-bit",
			Category: esphome.CategoryConfig,
		},
		Min: 1, Max: float64(setup.Present()), Step: 1,
		Mode: esphome.NumberBox,
	}

	d.minCores.OnCommand = func(v float32) {
		if err := setup.MinCores(int(v)); err != nil {
			slog.Error("holding cores online failed", "cores", int(v), "err", err)
			return
		}
		d.minCores.Set(v)
		if err := config.Set().Diag().MinCores(int(v)); err != nil {
			slog.Error("saving a setting failed", "setting", d.minCores.ObjectID, "err", err)
		}
		d.soon()
	}
}

func (d *Diag) soon() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

func (d *Diag) Run(ctx context.Context) error {
	// Amazon's firewall.sh flushes INPUT once boot completes, after Restore()
	// has already applied the saved switch: re-arm here so the OFF reject
	// (or the ON accept) is live for the whole resident phase, in the
	// component-run arming shape (card-14 rule: never Go-init-only).
	if err := d.reachable(d.ADB()); err != nil {
		slog.Error("re-arming remote adb failed", "open", d.ADB(), "err", err)
	}

	return collector.Run(
		ctx,
		func() time.Duration { return time.Duration(config.Get().Diag.Interval) * time.Second },
		d.wake,
		d.Sample,
	)
}

func (d *Diag) playback() {
	spk := speaker.Get()

	d.testPlayback = &esphome.Button{
		Base: esphome.Base{
			ObjectID: "test_playback",
			Name:     "Test playback",
			Icon:     "mdi:waveform",
			Category: esphome.CategoryDiagnostic,
		},
		OnPress: func() {
			slog.Info("test playback", "resampling", spk.Resampling(), "step", spk.Step())
			spk.PlayVoice(upsample.Sweep())
		},
	}
}

func (d *Diag) remote() {
	d.adb = &esphome.Switch{
		Base: esphome.Base{
			ObjectID: "remote_adb",
			Name:     "Remote adb",
			Icon:     "mdi:bug-outline",
			Category: esphome.CategoryDiagnostic,
		},
	}

	d.adb.OnCommand = func(on bool) {
		if err := d.reachable(on); err != nil {
			d.adb.Set(!on)
			return
		}
		if err := config.Set().Diag().ADB(on); err != nil {
			slog.Error("saving a setting failed", "setting", d.adb.ObjectID, "err", err)
		}
	}

	// The protocol's device info carries the MAC and no address.
	d.ip = &esphome.TextSensor{
		Base: esphome.Base{
			ObjectID: "ip_address",
			Name:     "IP address",
			Icon:     "mdi:ip-network",
			Category: esphome.CategoryDiagnostic,
		},
	}
	d.address()
}

func (d *Diag) ADB() bool { return d.adb.Get() }

func (d *Diag) SetADB(on bool) {
	d.adb.Set(on)
	d.adb.OnCommand(on)
}

func (d *Diag) reachable(on bool) error {
	change, what := func() error { return networkAdbClosed() }, "closing"
	if on {
		change, what = func() error { return networkAdbOpened() }, "opening"
	}

	if err := change(); err != nil {
		slog.Error(what+" the adb port failed", "port", adbPort, "err", err)
		return err
	}

	slog.Warn("remote adb", "open", on, "port", adbPort)
	d.adb.Set(on)
	return nil
}

// networkAdbOpened makes network adb reachable. Both the firewall accept (the
// ECHOLOCAL_ADB chain, jumped before the stock rules in INPUT) and adbd
// listening on tcp:5555 are required: the boot image sets
// service.adb.tcp.port and starts adbd, but Amazon's firewall.sh carries the
// only input rule for 5555, which the echod chain must be allowed to coexist
// with rather than replace (card E).
func networkAdbOpened() error {
	if err := firewall.Open(firewall.ADB, adbPort); err != nil {
		return err
	}
	if err := adbdListen(true); err != nil {
		return err
	}
	opened, err := firewall.Opened(firewall.ADB, adbPort)
	if err != nil {
		return err
	}
	if !opened {
		return fmt.Errorf("the %s chain lacks its accept rule after opening", firewall.ADB)
	}
	return nil
}

// networkAdbClosed makes network adb genuinely unreachable. Neither of the
// two halves alone is enough on this FireOS build: adbd listens on 5555
// unconditionally regardless of the service.adb.tcp.port value, and an empty
// or missing echod chain still leaves Amazon's own ACCEPT for 5555 in INPUT.
// So closed means: an explicit REJECT in ECHOLOCAL_ADB (the chain is jumped at
// INPUT position 1, ahead of the stock accepts) AND adbd stopped.
func networkAdbClosed() error {
	if err := firewall.Reject(firewall.ADB, adbPort); err != nil {
		return err
	}
	return adbdListen(false)
}

func adbdListen(tcp bool) error {
	what := "-1"
	if tcp {
		what = fmt.Sprint(adbPort)
	}
	// The property decides the mode adbd restarts in; restart to apply it.
	if err := prop.Set("service.adb.tcp.port", what); err != nil {
		return err
	}
	if err := prop.Restart("adbd"); err != nil {
		return err
	}

	port, err := prop.Get("service.adb.tcp.port")
	if err != nil {
		return err
	}
	if tcp && port != fmt.Sprint(adbPort) {
		return fmt.Errorf("service.adb.tcp.port is %q, want %d", port, adbPort)
	}
	if !tcp && port == fmt.Sprint(adbPort) {
		return fmt.Errorf("service.adb.tcp.port still %q after closing", port)
	}
	return nil
}

func (d *Diag) address() {
	var ips []string
	for _, ip := range netaddress.Addresses() {
		ips = append(ips, ip.String())
	}
	d.ip.Set(strings.Join(ips, ", "))
}

// The shell colour comes from the factory idme partition.
func (d *Diag) identity() {
	b := config.Get().Device.Board

	d.color = &esphome.TextSensor{
		Base: esphome.Base{
			ObjectID: "hardware_color",
			Name:     "Hardware color",
			Icon:     "mdi:palette",
			Category: esphome.CategoryDiagnostic,
		},
	}
	d.color.Set(b.Color(layout.Idme))

	d.boardName = &esphome.TextSensor{
		Base: esphome.Base{
			ObjectID: "hardware_board",
			Name:     "Hardware board",
			Icon:     "mdi:chip",
			Category: esphome.CategoryDiagnostic,
		},
	}
	d.boardName.Set(b.Codename)
}

// The protocol carries a state as a float32, exact in bytes only to 16 MB.
func (d *Diag) storage() {
	d.cache = wake.Lib().CacheEntities(inUse)
	d.free = &esphome.Sensor{
		Base: esphome.Base{
			ObjectID: "free_space",
			Name:     "Free space",
			Icon:     "mdi:harddisk",
			Category: esphome.CategoryDiagnostic,
		},
		Unit:        "kB",
		DeviceClass: "data_size",
		StateClass:  esphome.StateClassMeasurement,
	}
}

func (d *Diag) radio() {
	d.signal = &esphome.Sensor{
		Base: esphome.Base{
			ObjectID: "wifi_signal", Name: "Wifi signal", Icon: "mdi:wifi",
			Category: esphome.CategoryDiagnostic,
		},
		Unit:        "dBm",
		DeviceClass: "signal_strength",
		StateClass:  esphome.StateClassMeasurement,
	}

	rate := func(id, name, icon string) *esphome.Sensor {
		return &esphome.Sensor{
			Base: esphome.Base{
				ObjectID: id, Name: name, Icon: icon, Category: esphome.CategoryDiagnostic,
			},
			Unit:       "kB/s",
			StateClass: esphome.StateClassMeasurement,
			Decimals:   1,
		}
	}
	d.rxRate = rate("wifi_received", "Wifi received", "mdi:download-network")
	d.txRate = rate("wifi_sent", "Wifi sent", "mdi:upload-network")

	d.ads = &esphome.Sensor{
		Base: esphome.Base{
			ObjectID: "ble_advertisements", Name: "BLE advertisements", Icon: "mdi:bluetooth-audio",
			Category: esphome.CategoryDiagnostic,
		},
		Unit:       "/s",
		StateClass: esphome.StateClassMeasurement,
		Decimals:   1,
	}
}

// This kernel hotplugs cores.
func (d *Diag) hardware() {
	temp := func(id, name, icon string) *esphome.Sensor {
		return &esphome.Sensor{
			Base: esphome.Base{
				ObjectID: id, Name: name, Icon: icon, Category: esphome.CategoryDiagnostic,
			},
			Unit:        "°C",
			DeviceClass: "temperature",
			StateClass:  esphome.StateClassMeasurement,
			Decimals:    1,
		}
	}
	d.temperature = temp("cpu_temperature", "CPU temperature", "mdi:thermometer")
	d.radioTemp = temp("radio_temperature", "Radio temperature", "mdi:thermometer")

	count := func(id, name string) *esphome.Sensor {
		return &esphome.Sensor{
			Base: esphome.Base{
				ObjectID: id,
				Name:     name,
				Icon:     "mdi:cpu-64-bit",
				Category: esphome.CategoryDiagnostic,
			},
			StateClass: esphome.StateClassMeasurement,
		}
	}
	d.cores = count("cpu_cores", "CPU cores")
	d.coresOnline = count("cpu_cores_online", "CPU cores online")

	room := func(id, name, icon string) *esphome.Sensor {
		return &esphome.Sensor{
			Base: esphome.Base{
				ObjectID: id, Name: name, Icon: icon,
				DeviceID: component.DeviceMicrophone,
				Category: esphome.CategoryDiagnostic,
			},
			StateClass: esphome.StateClassMeasurement,
			Decimals:   2,
		}
	}
	d.roomLevel = room("room_level", "Room level", "mdi:waveform")
	d.roomFloor = room("room_floor", "Room floor", "mdi:arrow-collapse-down")

	d.load = &esphome.Sensor{
		Base: esphome.Base{
			ObjectID: "load_average", Name: "Load average", Icon: "mdi:gauge",
			Category: esphome.CategoryDiagnostic,
		},
		StateClass: esphome.StateClassMeasurement,
		Decimals:   2,
	}
	d.memory = &esphome.Sensor{
		Base: esphome.Base{
			ObjectID: "memory_available", Name: "Memory available", Icon: "mdi:memory",
			Category: esphome.CategoryDiagnostic,
		},
		Unit:        "kB",
		DeviceClass: "data_size",
		StateClass:  esphome.StateClassMeasurement,
	}

	d.luxPath = metrics.Reader{}.LuxPath()
	switch {
	case d.luxPath != "":
		slog.Info("light sensor", "at", d.luxPath)
	case Ambient != nil:
		slog.Info("light sensor", "at", "alsps")
	default:
		slog.Warn("no light sensor found")
	}
	d.lux = &esphome.Sensor{
		Base: esphome.Base{
			ObjectID: "lux", Name: "Lux", Icon: "mdi:brightness-6",
			Category: esphome.CategoryDiagnostic,
		},
		Unit:        "lx",
		DeviceClass: "illuminance",
		StateClass:  esphome.StateClassMeasurement,
	}
}

func (d *Diag) Measure() {
	d.cache.Measure()

	free, err := metrics.Free(layout.StateDir)
	if err != nil {
		slog.Error("reading free space failed", "dir", layout.StateDir, "err", err)
		return
	}
	d.free.Set(float32(free / 1024))
}

func (d *Diag) Restore(c config.Config) {
	d.Measure()

	d.interval.Set(float32(c.Diag.Interval))
	slog.Info("restored", "what", d.interval.ObjectID, "using", c.Diag.Interval)

	// The firewall chain is empty after a reboot but not after an echod restart.
	if err := d.reachable(c.Diag.ADB); err == nil {
		slog.Info("restored", "what", d.adb.ObjectID, "using", c.Diag.ADB)
	}

	if err := setup.MinCores(c.Diag.MinCores); err != nil {
		slog.Error("holding cores online failed", "cores", c.Diag.MinCores, "err", err)
	} else {
		d.minCores.Set(float32(c.Diag.MinCores))
		slog.Info("restored", "what", d.minCores.ObjectID, "using", c.Diag.MinCores)
	}
}

func (d *Diag) Sample() {
	d.Measure()
	d.board()
	d.address()
	d.wireless()
	d.room()
}

func (d *Diag) room() {
	source := mic.Get()
	d.roomLevel.Set(float32(source.Peak()))
	d.roomFloor.Set(float32(source.Floor()))
}

func (d *Diag) wireless() {
	signal, rx, tx := metrics.Reader{}.Wifi()
	set(d.signal, signal)

	reports := bluetooth.Get().Reports()

	now := time.Now()
	was := d.last
	d.last.at, d.last.rx, d.last.tx, d.last.reports, d.last.recorded = now, rx.Value, tx.Value, reports, true

	if !was.recorded {
		return
	}
	secs := now.Sub(was.at).Seconds()
	if secs <= 0 {
		return
	}

	if rx.Known {
		d.rxRate.Set(float32((rx.Value - was.rx) / secs / 1024))
	}
	if tx.Known {
		d.txRate.Set(float32((tx.Value - was.tx) / secs / 1024))
	}
	d.ads.Set(float32(float64(reports-was.reports) / secs))
}

func (d *Diag) board() {
	r := metrics.Reader{}

	temps := r.Temperatures()
	set(d.temperature, reading(temps, cpuZone))
	set(d.radioTemp, reading(temps, radioZone))

	present, online := r.Cores()
	set(d.cores, present)
	set(d.coresOnline, online)

	one, _ := r.Load()
	set(d.load, one)

	available, _ := r.Memory()
	set(d.memory, available)

	switch {
	case d.luxPath != "":
		set(d.lux, r.Lux(d.luxPath))
	case Ambient != nil:
		set(d.lux, Ambient())
	}
}

func reading(temps map[string]float64, zone string) metrics.Reading {
	v, ok := temps[zone]
	return metrics.Reading{Value: v, Known: ok}
}

func set(s *esphome.Sensor, r metrics.Reading) {
	if r.Known {
		s.Set(float32(r.Value))
	}
}

func inUse() []string { return config.Get().Wake.IDs(wakeword.Slots) }
