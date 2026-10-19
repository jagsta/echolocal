"""_make_send must not call hass.async_create_task from the relay's send
closure (t_ff6fb29d / t_b8865628 class). The relay's feed() runs from the
machine's sync emit thread; async_create_task off-loop is hard-raised by HA
(live 19 Oct). The send closure must hand off via hass.add_job, which is
thread-safe from both the loop and worker threads.
"""
from custom_components.echolocal_dropin.runtime import DropInRuntime


class OffLoopTrapHass:
    """Hass stand-in that FAILS async_create_task if it is ever used —
    the fix contract is that send() only ever goes through add_job."""

    def __init__(self):
        self.jobs = []

    def async_create_task(self, coro):
        raise RuntimeError(
            f"off-loop async_create_task used: {coro!r} "
            "(t_ff6fb29d contract: send() must hand off via add_job)"
        )

    def add_job(self, *args):
        self.jobs.append(("job", args))


def test_make_send_hands_off_via_add_job_not_async_create_task():
    rt = DropInRuntime.__new__(DropInRuntime)
    hass = OffLoopTrapHass()
    rt.hass = hass  # type: ignore[assignment]

    send = rt._make_send("sess-1")

    # Called from a SYNC context with no event loop running (the emit-thread
    # shape): async_create_task would hard-raise off-loop in production.
    send("dev-b", b"\x01\x02")

    assert hass.jobs == [("job", (rt._send_frame, "sess-1", "dev-b", b"\x01\x02"))]