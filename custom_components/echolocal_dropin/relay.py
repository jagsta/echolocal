"""Frame relay for the drop-in intercom (D-DI-4: HA is the frame relay).

Star topology: one relay per SESSION, endpoints in a list, one-to-N fan-out.
Each endpoint's frames are fanned to every OTHER endpoint — not a hardcoded
pairwise bridge (specs 44dacef: 3-way must be additive).

The transport is injected so tests can mock it; in production the
integration layer maps send/receive onto each device's existing ESPHome
native API connection (VoiceAssistantAudio frames, one perpetual turn per
device — specs/dropin-probe-findings.md §4).
"""
from __future__ import annotations

from typing import Callable

SendFn = Callable[[str, bytes], None]  # (device_id, pcm_frame)


class StarRelay:
    """Frame relay: N endpoints, fan-out to all others. One stream per device."""

    def __init__(self) -> None:
        self._endpoints: list[str] = []
        self._send: SendFn | None = None
        self._open = False

    def bind(self, send: SendFn) -> None:
        self._send = send

    def add(self, device_id: str) -> None:
        if device_id in self._endpoints:
            raise RelayError(f"{device_id} already in relay")
        self._endpoints.append(device_id)

    def endpoints(self) -> list[str]:
        return list(self._endpoints)

    def open(self) -> None:
        if self._send is None:
            raise RelayError("transport not bound")
        self._open = True

    def close(self) -> None:
        self._open = False
        self._endpoints.clear()

    def is_open(self) -> bool:
        return self._open

    def feed(self, from_device: str, frame: bytes) -> int:
        """Fan a frame from one endpoint out to every other endpoint.

        Returns the number of peers the frame was delivered to.
        """
        if not self._open:
            raise RelayError("relay is closed")
        if from_device not in self._endpoints:
            raise RelayError(f"{from_device} is not a relay endpoint")
        if not frame:
            return 0
        peers = [d for d in self._endpoints if d != from_device]
        for peer in peers:
            assert self._send is not None
            self._send(peer, frame)
        return len(peers)


class RelayError(Exception):
    pass


class FrameCodec:
    """Minimal codec over VoiceAssistantAudio payload bytes.

    The device side (card 2) owns the wire framing; HA just relays opaque
    PCM payloads. A frame marker of 0 length means end-of-utterance is NOT
    used here — End is signalled on the wire separately (voice.go End flag),
    so the relay carries payloads verbatim.
    """

    @staticmethod
    def encode(payload: bytes) -> bytes:
        return bytes(payload)

    @staticmethod
    def decode(payload: bytes) -> bytes:
        return bytes(payload)