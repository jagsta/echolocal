package api_test

import (
	"slices"
	"testing"

	esphome "github.com/ygelfand/go-esphome-device"

	"github.com/ygelfand/libcountertop/pkg/homeassistant/nativeapi"

	"github.com/ygelfand/echolocal/internal/component"
	_ "github.com/ygelfand/echolocal/internal/component/all"
	"github.com/ygelfand/echolocal/internal/feature/api"
)

func ids(ds []esphome.Device) []uint32 {
	var out []uint32
	for _, d := range ds {
		out = append(out, d.ID)
	}
	return out
}

func TestTheDotAnnouncesEverySubDevice(t *testing.T) {
	got := ids(nativeapi.SubDevices(api.Devices("dot"), component.Default().Entities()))
	want := []uint32{component.DeviceRing, component.DeviceMicrophone, component.DevicePlayback,
		component.DeviceVoice, component.DeviceFrontend,
		component.AssistantDevice(0), component.AssistantDevice(1)}
	if !slices.Equal(got, want) {
		t.Errorf("announced %v, want %v", got, want)
	}
}
