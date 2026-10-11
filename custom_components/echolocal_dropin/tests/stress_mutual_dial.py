"""Stress harness: mutual-dial lower-device-id determinism under concurrency.

Runs 80 iterations of concurrent opposite-direction dials with alternating
submission orders; asserts a single live session whose caller is always the
lower device-id and whose endpoint states are dev-a=DIALING, dev-b=RINGING.
Prints STRESS-OK or the failure count.
"""
import concurrent.futures
import sys

from custom_components.echolocal_dropin.const import STATE_DIALING, STATE_RINGING
from custom_components.echolocal_dropin.state_machine import CallError, Endpoint
from custom_components.echolocal_dropin.tests.test_failure_modes import make_machine


def main() -> int:
    fails = 0
    for rev in range(80):
        m, _, _ = make_machine()
        pairs = (
            [("dev-b", "dev-a"), ("dev-a", "dev-b")]
            if rev % 2 == 0
            else [("dev-a", "dev-b"), ("dev-b", "dev-a")]
        )

        def dial(p):
            try:
                m.call("s-x", Endpoint(p[0], "kitchen"), Endpoint(p[1], "living"))
                return "ok"
            except CallError:
                return "busy"

        with concurrent.futures.ThreadPoolExecutor(max_workers=2) as pool:
            list(pool.map(dial, pairs))
        s = next(iter(m.sessions.values()), None)
        if (
            s is None
            or s.endpoints[0].device_id != "dev-a"
            or len(m.sessions) != 1
            or m.endpoint_state("dev-a") != STATE_DIALING
            or m.endpoint_state("dev-b") != STATE_RINGING
        ):
            fails += 1
    print("STRESS-OK" if fails == 0 else f"FAILURES: {fails}/80")
    return 0 if fails == 0 else 1


if __name__ == "__main__":
    sys.exit(main())