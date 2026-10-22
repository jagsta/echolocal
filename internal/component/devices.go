package component

// Home Assistant keys a sub-device on the device address and this number.
const (
	DeviceRing uint32 = iota + 1

	DeviceMicrophone

	DevicePlayback

	DeviceAssistant
)

const DeviceScreen uint32 = 100

const DeviceCamera uint32 = 101

const DeviceDashboard uint32 = 102

const DeviceVoice uint32 = 103

const DeviceFrontend uint32 = 104

func AssistantDevice(slot int) uint32 { return DeviceAssistant + uint32(slot) }
