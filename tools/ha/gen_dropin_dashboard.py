#!/usr/bin/python3
"""Generate the Drop-in dashboard tiles YAML from HA's own registries.

Reads (via SSH to the HA host, as root):
  - /config/.storage/core.config_entries   (esphome entries = one per dot:
                                            device_name, host, unique_id=MAC)
  - /config/.storage/core.device_registry  (esphome devices -> area_id)
  - /config/.storage/core.area_registry    (area_id -> area name)
  - /config/.storage/lovelace.drop_in      (the live Drop-in dashboard)

Emits (to stdout or --out FILE):
  - YAML for a sections-layout Drop-in dashboard with one live mushroom tile
    per discovered dot, plus optional manual "pending" placeholder entries so
    future rooms render disabled until their dot exists (D-DI-9d: no
    auto-dial; the page's confirm button is the dial gate).

Design contract (specs/dropin-dashboard.yaml, D-DI-9c):
  - sig=https://homeassistant.jagbains.org is MANDATORY in every tile URL.
  - host=<dot-ip-or-name> is embedded per tile; DHCP-reserved per dot so the
    IP is stable (see runbook).
  - Caller identity never comes from the tile (D-DI-9b).

Usage:
  python3 gen_dropin_dashboard.py            # print YAML to stdout
  python3 gen_dropin_dashboard.py --out /tmp/dropin.yaml
  python3 gen_dropin_dashboard.py --pending lounge,zak,finn,jasmine
                                   ^ extra disabled tiles to render
  python3 gen_dropin_dashboard.py --host root@192.168.10.83   # override

Read-only against HA. Never writes to HA config — the paste-back into the HA
dashboard YAML editor is deliberately manual.
"""
from __future__ import annotations

import argparse
import os
import json
import subprocess
import sys
import sysconfig  # noqa: F401  (ensures stdlib layout docs)

HA_HOST_DEFAULT = "root@192.168.10.83"
SIG = "https://homeassistant.jagbains.org"

DOT_SIG_TMPL = "https://%s-dot.jagbains.org"
PAGE_BASE = "https://homeassistant.jagbains.org:8123/local/phone-dropin.html"

# Per-room display names + icons pending-room defaults; live tiles take the
# device name + area name from the registries.
PENDING_ICONS = {
    "lounge": "mdi:sofa-outline",
    "zak": "mdi:bed-single-outline",
    "finn": "mdi:bed-single-outline",
    "jasmine": "mdi:teddy-bear",
}
DEFAULT_ICON = "mdi:microphone"

STORAGE = {
    "entries": "/config/.storage/core.config_entries",
    "devices": "/config/.storage/core.device_registry",
    "areas": "/config/.storage/core.area_registry",
}


def scp_read(ha_host: str, remote_path: str, local_tmp: str) -> dict:
    subprocess.run(
        ["scp", "-o", "BatchMode=yes", f"{ha_host}:{remote_path}", local_tmp],
        check=True, capture_output=True, timeout=30,
    )
    with open(local_tmp) as f:
        doc = json.load(f)
    # .storage wrappers: {"key": ..., "data": {"entities|entries|devices|areas": [...]}}
    for v in doc.values():
        if isinstance(v, dict) and (v.get("items") is not None or v.get("devices") is not None
                                    or v.get("entries") is not None or v.get("areas") is not None
                                    or v.get("entity_categories") is not None
                                    or v.get("entities") is not None):
            return v
    return doc


def collect(ha_host: str, tmpdir: str) -> tuple[list[dict], dict, dict]:
    """Dots: [{name, host, mac}], areas: {area_id: name}, dev_area: {mac: area_id}"""
    entries = scp_read(ha_host, STORAGE["entries"], f"{tmpdir}/entries.json")
    devices = scp_read(ha_host, STORAGE["devices"], f"{tmpdir}/devices.json")
    areas = scp_read(ha_host, STORAGE["areas"], f"{tmpdir}/areas.json")

    esphome = [e for e in entries.get("entries", []) if e.get("domain") == "esphome"]
    dots = []
    for e in esphome:
        data = e.get("data") or {}
        # The dot's echo of itself is the entry whose unique_id matches the MAC
        # of its esphome devices; device_name is our install --name argument.
        dots.append({
            "name": data.get("device_name") or e.get("title", "").lower(),
            "host": data.get("host") or "",
            "mac": (e.get("unique_id") or "").upper(),
        })
    echomacs = {ident[1].split("_")[0].upper()
                for d in devices.get("devices", [])
                if (d.get("manufacturer") or "") == "EchoLocal"
                for ident in (d.get("identifiers") or [])
                if isinstance(ident, list) and len(ident) > 1 and ident[0] == "esphome"}
    dots = [d for d in dots if d["mac"] in echomacs]  # only EchoLocal-made devices = drop-in dots

    area_names = {a["area_id"]: a.get("name", a["area_id"])
                  for a in areas.get("areas", []) if a.get("area_id")}
    dev_area = {}
    for d in devices.get("devices", []):
        for ident in d.get("identifiers", []) or []:
            if isinstance(ident, list) and ident and ident[0] == "esphome":
                mac = (ident[1] or "").split("_")[0].upper()
                if mac:
                    dev_area[mac] = d.get("area_id")
    return dots, dev_area, area_names


def live_tile(name: str, disp: str, host_ip: str, icon: str) -> dict:
    # D-CERT-5: per-dot hostname = room name + -dot.jagbains.org; the tile's
    # sig= is now the DOT's own trusted origin (wildcard LE via certfetch),
    # NOT the single homeassistant host. LEGACY_SIG=1 in the env falls back
    # to the old single-origin sig (NPM-dormant rollback path, D-CERT-6).
    sig = SIG if os.environ.get("LEGACY_SIG") else DOT_SIG_TMPL % name
    return {
        "type": "custom:mushroom-template-card",
        "primary": f"{disp}",
        "secondary": "Drop in — live",
        "icon": icon,
        "icon_color": "blue",
        "fill_container": True,
        "tap_action": {
            "action": "url",
            "url_path": f"{PAGE_BASE}?host={host_ip}&room={name}"
                        f"&name={disp}&sig={sig}",
        },
        "card_mod": {"style": "ha-card { --ha-card-background:"
                              " var(--card-background-color); }\n"
                              ".multiline { height: 64px; }\n"},
    }


def pending_tile(name: str, disp: str) -> dict:
    return {
        "type": "custom:mushroom-template-card",
        "primary": disp,
        "secondary": "dot pending",
        "icon": PENDING_ICONS.get(name, DEFAULT_ICON),
        "icon_color": "",
        "fill_container": True,
        "tap_action": {"action": "none"},
        "card_mod": {"style": "ha-card { opacity: 0.45; }\n"
                              ".multiline { height: 64px; }\n"},
    }


def build_dashboard(dots: list[dict], dev_area: dict,
                    area_names: dict, pending: list[str]) -> dict:
    live, pending_cards = [], []
    seen = set()
    for d in dots:
        key = d["name"].lower()
        seen.add(key)
        area_id = dev_area.get(d["mac"])
        disp = area_names.get(area_id, d["name"].capitalize()) if area_id else d["name"].capitalize()
        if d["host"] and d["name"]:
            live.append(live_tile(key, disp, d["host"], DEFAULT_ICON))
    for name in pending:
        key = name.lower()
        if key in seen:
            continue  # dot already present — no placeholder needed
        disp = area_names.get(name, name.capitalize()) if name in area_names else name.capitalize()
        pending_cards.append(pending_tile(key, disp))

    cards = live + pending_cards
    # D-DI-9c layout: first row live, remaining as second section (matches the
    # deployed dashboard's shape: row 1 kitchen+lounge, row 2 kids).
    first, second = cards[:2], cards[2:]
    sections = [{"type": "grid", "cards": first}]
    if second:
        sections.append({"type": "grid", "cards": second})
    return {
        "title": "Drop-in",
        "views": [{
            "title": "Drop-in",
            "path": "drop-in",
            "icon": "mdi:phone-dial",
            "type": "sections",
            "sections": sections,
        }],
    }



def to_yaml(doc: dict) -> str:
    """Emit the dashboard doc as YAML the HA raw editor accepts. Hand-rolled:
    the target env has no PyYAML (cron venv pitfall) and the shape is fixed."""
    v = doc["views"][0]
    L = ['title: "Drop-in"', "views:"]
    L.append(f'  - title: "{v["title"]}"')
    L.append(f'    path: "{v["path"]}"')
    L.append(f'    icon: "{v["icon"]}"')
    L.append(f'    type: {v["type"]}')
    L.append("    sections:")
    for sec in v["sections"]:
        L.append("      - type: grid")
        L.append("        cards:")
        for c in sec["cards"]:
            style = c["card_mod"]["style"].replace("\n", "\n                ")
            L.append("            - type: custom:mushroom-template-card")
            L.append(f'              primary: "{c["primary"]}"')
            L.append(f'              secondary: "{c["secondary"]}"')
            L.append(f'              icon: "{c["icon"]}"')
            L.append(f'              icon_color: "{c["icon_color"]}"')
            L.append("              fill_container: true")
            L.append("              tap_action:")
            L.append(f'                action: {c["tap_action"]["action"]}')
            if "url_path" in c["tap_action"]:
                L.append('                url_path: >-')
                L.append(f'                  {c["tap_action"]["url_path"]}')
            L.append("              card_mod:")
            L.append("                style: |")
            for line in c["card_mod"]["style"].splitlines():
                L.append("                  " + line)
    return "\n".join(L) + "\n"


def main() -> int:
    ap = argparse.ArgumentParser(description=(__doc__ or "").splitlines()[0])
    ap.add_argument("--host", default=HA_HOST_DEFAULT,
                    help=f"SSH target of the HA host (default {HA_HOST_DEFAULT})")
    ap.add_argument("--pending", default="",
                    help="comma-separated room names to render as pending tiles")
    ap.add_argument("--out", help="write YAML here instead of stdout")
    args = ap.parse_args()

    import os, tempfile
    with tempfile.TemporaryDirectory(dir=os.environ.get("HERMES_TMP", "/tmp")) as td:
        dots, dev_area, area_names = collect(args.host, td)

    doc = build_dashboard(dots, dev_area, area_names,
                          [x.strip() for x in args.pending.split(",") if x.strip()])
    out = to_yaml(doc)
    if args.out:
        with open(args.out, "w") as f:
            f.write(out)
        print(f"wrote {args.out}")
    else:
        sys.stdout.write(out)
    return 0


if __name__ == "__main__":
    raise SystemExit(main())