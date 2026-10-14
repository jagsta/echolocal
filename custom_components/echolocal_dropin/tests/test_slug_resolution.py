"""Card A test: runtime resolves aspirational echod-<area> callee slugs to the
pinned device id before dialing (device transport only knows real ids)."""
import sys

sys.path.insert(0, ".")

from custom_components.echolocal_dropin.runtime import DropInRuntime
from custom_components.echolocal_dropin.device_map import AREA_TO_DEVICE
from custom_components.echolocal_dropin.state_machine import Endpoint


class FakeHass:
    class _States:
        def get(self, entity_id):
            return None
    states = _States()
    class _Services:
        def call(self, *a, **k):
            pass
        def async_register(self, *a, **k):
            pass
    services = _Services()
    def async_add_executor_job(self, fn, *a):
        return fn(*a)


def test_echod_slug_resolved_to_real_device():
    rt = DropInRuntime(FakeHass(), None)
    kitchen_dev = AREA_TO_DEVICE["kitchen"]
    rt._call_service({
        "caller_device": "acf8f22077b452856f9ef46024f95d57",
        "caller_room": "kitchen",
        "callee_device": "echod-little_lounge",  # aspirational slug
        "callee_room": "little_lounge",
        "session_id": "di-slugtest",
    })
    session = rt.machine.sessions["di-slugtest"]
    assert session.endpoint(kitchen_dev) is not None
    lounge_dev = AREA_TO_DEVICE["little_lounge"]
    assert session.endpoint(lounge_dev).state == "ringing"
    print("slug resolution OK:", kitchen_dev, "->", lounge_dev)