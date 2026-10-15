"""Card B (t_23673b6d): the deployed voice-UX automations must stay
device-agnostic and single-per-sentence-set.

HA collapses identical `command:` lists across sentence-trigger automations into
one trigger registration, so per-dot copies silently made one dot's "answer",
"hang up" and "drop in on …" unreachable (live 15 Oct 2026). These tests pin the
shape that fixes it: one automation per sentence set, the speaking satellite's
device id passed through as trigger.device_id, and the idle gate left to the
code guard in runtime.py + intent_router.py.
"""
from __future__ import annotations

import re
from pathlib import Path

import yaml

REPO_ROOT = Path(__file__).resolve().parents[3]
AUTOMATIONS_YAML = REPO_ROOT / "tools" / "ha" / "dropin_voice_automations.yaml"
DEVICE_ID = re.compile(r"\b[0-9a-f]{32}\b")


def automations() -> list[dict]:
    doc = yaml.safe_load(AUTOMATIONS_YAML.read_text())
    assert isinstance(doc, list)
    return doc


def by_id() -> dict[str, dict]:
    return {a["id"]: a for a in automations()}


def test_expected_verbs_present() -> None:
    assert set(by_id()) == {
        "echolocal_dropin_dial",
        "echolocal_dropin_answer",
        "echolocal_dropin_hangup",
    }


def test_no_hardcoded_dot_device_ids_or_conditions() -> None:
    """A per-dot device id (or a per-dot trigger condition) is what broke
    routing; the speaking satellite's id must come from trigger.device_id."""
    for auto in automations():
        blob = yaml.safe_dump(auto)
        assert not DEVICE_ID.search(blob), auto["id"]
        assert "condition" not in auto, auto["id"]
        assert "trigger.device_id" in blob, auto["id"]


def test_one_automation_per_sentence_set() -> None:
    """Two automations with the same command list = one of them is dead."""
    seen: dict[tuple, str] = {}
    for auto in automations():
        for trig in auto["trigger"]:
            assert trig["platform"] == "conversation", auto["id"]
            key = tuple(trig["command"])
            assert key not in seen, f"{auto['id']} duplicates {seen[key]}"
            seen[key] = auto["id"]


def test_verbs_map_to_the_voice_services() -> None:
    autos = by_id()
    expected = {
        "echolocal_dropin_dial": "echolocal_dropin.dial",
        "echolocal_dropin_answer": "echolocal_dropin.voice_answer",
        "echolocal_dropin_hangup": "echolocal_dropin.voice_hangup",
    }
    for auto_id, service in expected.items():
        calls = [step["action"] for step in autos[auto_id]["action"] if "action" in step]
        assert service in calls, (auto_id, calls)


def test_dial_normalises_the_spoken_room() -> None:
    """The trigger slot is raw spoken text; area ids are underscore-canonical
    (AREA_TO_DEVICE keys). A space-separated room resolves to no dot and wedges
    the caller in dialing until the ring timeout (live 15 Oct 2026)."""
    dial = by_id()["echolocal_dropin_dial"]
    variables = next(step["variables"] for step in dial["action"] if "variables" in step)
    assert 'replace(" ", "_")' in variables["callee_area"]


def test_answer_and_hangup_phrases_unchanged() -> None:
    """The spoken vocabulary is settled (D-DI-5): answer / hang up / stop."""
    autos = by_id()
    answer_cmds = autos["echolocal_dropin_answer"]["trigger"][0]["command"]
    hangup_cmds = autos["echolocal_dropin_hangup"]["trigger"][0]["command"]
    assert "answer" in answer_cmds
    assert "hang up" in hangup_cmds
    assert "stop" in hangup_cmds  # stop-as-hangup, bounded by the idle guard
