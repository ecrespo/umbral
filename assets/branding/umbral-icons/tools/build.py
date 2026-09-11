#!/usr/bin/env python3
"""Build every Umbral icon artifact from the SVG sources in svgs.py.

Usage: python3 tools/build.py [--app-id io.github.ecrespo.Umbral] [--out DIST]
Requires: cairosvg, Pillow.
"""
import argparse
import io
import struct
from pathlib import Path

import cairosvg
from PIL import Image

import svgs


def render(svg_text: str, size: int) -> bytes:
    return cairosvg.svg2png(bytestring=svg_text.encode(), output_width=size, output_height=size)


def object_svg_for(size: int) -> str:
    """Pick the hand-hinted source for small sizes, the 128 master otherwise."""
    if size <= 16:
        return svgs.object_16()
    if size <= 24:
        return svgs.object_24()
    return svgs.object_128()


def write(path: Path, data):
    path.parent.mkdir(parents=True, exist_ok=True)
    if isinstance(data, str):
        path.write_text(data, encoding="utf-8")
    else:
        path.write_bytes(data)


def pack_ico(pngs: dict[int, bytes]) -> bytes:
    """ICO with PNG-compressed entries (supported since Windows Vista)."""
    sizes = sorted(pngs)
    header = struct.pack("<HHH", 0, 1, len(sizes))
    offset = 6 + 16 * len(sizes)
    entries, blobs = b"", b""
    for s in sizes:
        data = pngs[s]
        dim = 0 if s >= 256 else s
        entries += struct.pack("<BBBBHHII", dim, dim, 0, 0, 1, 32, len(data), offset)
        blobs += data
        offset += len(data)
    return header + entries + blobs


ICNS_TYPES = [  # (OSType, pixel size)
    (b"icp4", 16), (b"icp5", 32), (b"icp6", 64), (b"ic07", 128), (b"ic08", 256),
    (b"ic09", 512), (b"ic10", 1024), (b"ic11", 32), (b"ic12", 64), (b"ic13", 256), (b"ic14", 512),
]


def pack_icns(svg_text: str) -> bytes:
    cache: dict[int, bytes] = {}
    chunks = b""
    for ostype, px in ICNS_TYPES:
        cache.setdefault(px, render(svg_text, px))
        data = cache[px]
        chunks += ostype + struct.pack(">I", len(data) + 8) + data
    return b"icns" + struct.pack(">I", len(chunks) + 8) + chunks


def padded(png: bytes, canvas: int, ratio: float) -> bytes:
    """Center an icon on a transparent square (Windows tile assets)."""
    icon = Image.open(io.BytesIO(png)).convert("RGBA")
    inner = round(canvas * ratio)
    icon = icon.resize((inner, inner), Image.LANCZOS)
    out = Image.new("RGBA", (canvas, canvas), (0, 0, 0, 0))
    out.paste(icon, ((canvas - inner) // 2, (canvas - inner) // 2), icon)
    buf = io.BytesIO()
    out.save(buf, "PNG", optimize=True)
    return buf.getvalue()


DESKTOP = """[Desktop Entry]
Type=Application
Name=Umbral
GenericName=Agentic Terminal
Comment=Terminal with agents and local models
Exec=umbral-desktop %U
Icon={app_id}
Terminal=false
Categories=System;TerminalEmulator;Development;
Keywords=terminal;shell;agent;ai;llm;ollama;
StartupNotify=true
StartupWMClass={app_id}
"""


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--app-id", default="io.github.ecrespo.Umbral")
    ap.add_argument("--out", default="dist")
    a = ap.parse_args()
    out, app_id = Path(a.out), a.app_id

    # --- sources ---------------------------------------------------------
    src = out / "source"
    sources = {
        "umbral-object-128.svg": svgs.object_128(),
        "umbral-object-24.svg": svgs.object_24(),
        "umbral-object-16.svg": svgs.object_16(),
        "umbral-symbolic-16.svg": svgs.symbolic_16(),
        "umbral-breeze-48.svg": svgs.breeze_48(),
        "umbral-macos-tile-1024.svg": svgs.mac_flat_tile(),
        "umbral-macos-legacy-1024.svg": svgs.mac_legacy_icns(),
        "concept-b-penumbra.svg": svgs.concept_penumbra(),
    }
    for name, text in sources.items():
        write(src / name, text)

    # --- Linux: hicolor theme --------------------------------------------
    hic = out / "linux" / "share" / "icons" / "hicolor"
    for s in (16, 22, 24, 32, 48, 64, 128, 256, 512):
        write(hic / f"{s}x{s}" / "apps" / f"{app_id}.png", render(object_svg_for(s), s))
    write(hic / "scalable" / "apps" / f"{app_id}.svg", svgs.object_128())
    write(hic / "symbolic" / "apps" / f"{app_id}-symbolic.svg", svgs.symbolic_16())
    write(out / "linux" / "share" / "applications" / f"{app_id}.desktop", DESKTOP.format(app_id=app_id))
    # Optional Breeze-styled asset for KDE-specific packaging
    write(out / "linux" / "kde-breeze" / "umbral-breeze-48.svg", svgs.breeze_48())
    for s in (22, 32, 48, 96):
        write(out / "linux" / "kde-breeze" / f"umbral-breeze-{s}.png", render(svgs.breeze_48(), s))

    # --- Windows ---------------------------------------------------------
    win = out / "windows"
    ico_sizes = (16, 20, 24, 32, 40, 48, 64, 256)
    write(win / "umbral.ico", pack_ico({s: render(object_svg_for(s), s) for s in ico_sizes}))
    assets = win / "Assets"
    for scale, px in ((100, 44), (125, 55), (150, 66), (200, 88), (400, 176)):
        write(assets / f"Square44x44Logo.scale-{scale}.png", render(object_svg_for(px), px))
    for px in (16, 24, 32, 48, 256):
        png = render(object_svg_for(px), px)
        write(assets / f"Square44x44Logo.targetsize-{px}.png", png)
        write(assets / f"Square44x44Logo.targetsize-{px}_altform-unplated.png", png)
    for scale, px in ((100, 150), (200, 300), (400, 600)):
        write(assets / f"Square150x150Logo.scale-{scale}.png", padded(render(svgs.object_128(), 512), px, 0.66))
    for scale, px in ((100, 50), (200, 100)):
        write(assets / f"StoreLogo.scale-{scale}.png", render(object_svg_for(px), px))

    # --- macOS -------------------------------------------------------------
    mac = out / "macos"
    write(mac / "umbral.icns", pack_icns(svgs.mac_legacy_icns()))
    write(mac / "appicon-1024-legacy.png", render(svgs.mac_legacy_icns(), 1024))
    layers = mac / "icon-composer-layers"
    write(layers / "0-background.svg", svgs.mac_background())
    write(layers / "1-spill-and-sill.svg", svgs.mac_spill())
    write(layers / "2-door-light.svg", svgs.mac_door())
    write(layers / "3-prompt.svg", svgs.mac_prompt())
    write(layers / "flat-preview-1024.png", render(svgs.mac_flat_tile(), 1024))

    # --- Wails v3 build dir --------------------------------------------------
    wb = out / "wails-build"
    write(wb / "appicon.png", render(svgs.object_128(), 1024))
    write(wb / "windows" / "icon.ico", (win / "umbral.ico").read_bytes())
    write(wb / "darwin" / "icons.icns", (mac / "umbral.icns").read_bytes())

    print(f"built into {out.resolve()}")


if __name__ == "__main__":
    main()
