#!/usr/bin/env python3
"""Presentation sheet built from the real rendered assets in dist/."""
import base64
import io
from pathlib import Path

import cairosvg
from PIL import Image

import svgs

D = Path("dist")
APP = "io.github.ecrespo.Umbral"


def png_uri(png: bytes) -> str:
    return "data:image/png;base64," + base64.b64encode(png).decode()


def file_png(path: Path, size: int | None = None) -> bytes:
    im = Image.open(path).convert("RGBA")
    if size and im.size[0] != size:
        im = im.resize((size, size), Image.LANCZOS)
    buf = io.BytesIO()
    im.save(buf, "PNG")
    return buf.getvalue()


def hic(size: int) -> bytes:
    return file_png(D / "linux/share/icons/hicolor" / f"{size}x{size}" / "apps" / f"{APP}.png")


def svg_png(svg_text: str, size: int) -> bytes:
    return cairosvg.svg2png(bytestring=svg_text.encode(), output_width=size, output_height=size)


def img(png: bytes, x, y, s):
    return f'<image href="{png_uri(png)}" x="{x}" y="{y}" width="{s}" height="{s}"/>'


def label(x, y, text, color="#6B6880", size=20, anchor="start"):
    return (f'<text x="{x}" y="{y}" font-family="Geist Mono" font-size="{size}" fill="{color}" '
            f'text-anchor="{anchor}" letter-spacing="1">{text}</text>')


W, H = 2400, 1700
parts = [f'<rect width="{W}" height="{H}" fill="#F4F2EE"/>']

# Title band
parts.append('<text x="120" y="150" font-family="Jura" font-weight="300" font-size="72" '
             'fill="#1E1A52" letter-spacing="28">UMBRAL</text>')
parts.append(label(124, 196, "app icon identity · v0.1 · 2026-09-11", "#8A8698", 22))
parts.append(label(2280, 196, "door · light · prompt · threshold", "#8A8698", 22, "end"))

# Hero: Linux object on light, macOS tile on dark
parts.append('<rect x="120" y="230" width="1020" height="640" rx="24" fill="#ECE9F5"/>')
parts.append(img(hic(512), 374, 270, 512))
parts.append(label(150, 845, "linux · hicolor scalable · object on 128 px grid"))
parts.append('<rect x="1260" y="230" width="1020" height="640" rx="24" fill="#0E0C24"/>')
parts.append(img(file_png(D / "macos/appicon-1024-legacy.png", 580), 1480, 250, 580))
parts.append(label(1290, 845, "macos · 1024 tile · Icon Composer layers", "#9C97D9"))

# Row 2: four platform cards
cards = [120, 662, 1204, 1746]
cw, cy, ch = 534, 900, 360

# GNOME symbolic
x = cards[0]
parts.append(f'<rect x="{x}" y="{cy}" width="{cw}" height="{ch}" rx="20" fill="#FFFFFF"/>')
sym = svgs.symbolic_16()
parts.append(img(svg_png(sym, 128), x + 60, cy + 90, 128))
parts.append(img(svg_png(sym, 64), x + 240, cy + 122, 64))
parts.append(img(svg_png(sym, 32), x + 350, cy + 138, 32))
parts.append(img(svg_png(sym, 16), x + 430, cy + 146, 16))
parts.append(label(x + 30, cy + ch - 30, "gnome · symbolic 16 · recolorable"))

# KDE Breeze
x = cards[1]
parts.append(f'<rect x="{x}" y="{cy}" width="{cw}" height="{ch}" rx="20" fill="#E3E7EC"/>')
parts.append(img(file_png(D / "linux/kde-breeze/umbral-breeze-96.png"), x + 60, cy + 70, 144))
parts.append(img(file_png(D / "linux/kde-breeze/umbral-breeze-48.png"), x + 260, cy + 118, 48))
parts.append(img(file_png(D / "linux/kde-breeze/umbral-breeze-32.png"), x + 350, cy + 126, 32))
parts.append(img(file_png(D / "linux/kde-breeze/umbral-breeze-22.png"), x + 422, cy + 131, 22))
parts.append(label(x + 30, cy + ch - 30, "kde · breeze 48 · 45° shadow"))

# XFCE / LXDE panel + menu
x = cards[2]
parts.append(f'<rect x="{x}" y="{cy}" width="{cw}" height="{ch}" rx="20" fill="#FFFFFF"/>')
parts.append(f'<rect x="{x + 30}" y="{cy + 40}" width="{cw - 60}" height="44" rx="6" fill="#2B2A33"/>')
for i in range(5):
    parts.append(f'<rect x="{x + 50 + i * 40}" y="{cy + 52}" width="20" height="20" rx="4" fill="#4A4856"/>')
parts.append(img(hic(24), x + 250, cy + 50, 24))
parts.append(f'<rect x="{x + 30}" y="{cy + 110}" width="{cw - 60}" height="150" rx="6" fill="#F1F0F4"/>')
for i, name in enumerate(["Umbral", "Files", "Editor"]):
    yy = cy + 128 + i * 42
    if i == 0:
        parts.append(f'<rect x="{x + 36}" y="{yy - 6}" width="{cw - 72}" height="34" rx="4" fill="#DAD6F2"/>')
        parts.append(img(hic(16), x + 52, yy + 3, 16))
    else:
        parts.append(f'<rect x="{x + 52}" y="{yy + 3}" width="16" height="16" rx="3" fill="#C9C7D1"/>')
    parts.append(f'<text x="{x + 82}" y="{yy + 17}" font-family="Instrument Sans" font-size="18" fill="#2B2A33">{name}</text>')
parts.append(label(x + 30, cy + ch - 30, "xfce · lxde · panel 24 · menu 16"))

# Windows taskbars
x = cards[3]
parts.append(f'<rect x="{x}" y="{cy}" width="{cw}" height="{ch}" rx="20" fill="#FFFFFF"/>')
for row, (bg, dot) in enumerate([("#F3F3F3", "#D0D0D0"), ("#202020", "#3A3A3A")]):
    yy = cy + 40 + row * 110
    parts.append(f'<rect x="{x + 30}" y="{yy}" width="{cw - 60}" height="72" rx="8" fill="{bg}"/>')
    for i in range(6):
        cx = x + 90 + i * 64
        if i == 3:
            parts.append(img(hic(32), cx - 16, yy + 20, 32))
            parts.append(f'<rect x="{cx - 8}" y="{yy + 62}" width="16" height="3" rx="1.5" fill="#6A63D0"/>')
        else:
            parts.append(f'<rect x="{cx - 12}" y="{yy + 24}" width="24" height="24" rx="5" fill="{dot}"/>')
parts.append(label(x + 30, cy + ch - 30, "windows · ico 16–256 · light / dark"))

# Row 3: size ladder light + dark
ly, lh = 1300, 330
sizes = [16, 22, 24, 32, 48, 64, 128]
for i, (bx, bg, fg) in enumerate([(120, "#FFFFFF", "#6B6880"), (1204, "#1C1B22", "#9C97D9")]):
    parts.append(f'<rect x="{bx}" y="{ly}" width="1076" height="{lh}" rx="20" fill="{bg}"/>')
    total = sum(sizes) + 60 * (len(sizes) - 1)
    xx = bx + (1076 - total) / 2
    base = ly + 220
    for s in sizes:
        parts.append(img(hic(s), xx, base - s, s))
        parts.append(label(xx + s / 2, base + 50, str(s), fg, 18, "middle"))
        xx += s + 60
parts.append(label(2280, 1675, "16 and 24 px hand-drawn · the rest from the 128 master", "#8A8698", 18, "end"))

svg = f'<svg xmlns="http://www.w3.org/2000/svg" width="{W}" height="{H}" viewBox="0 0 {W} {H}">' + "".join(parts) + "</svg>"
out = D / "preview"
out.mkdir(parents=True, exist_ok=True)
(out / "umbral-icon-sheet.png").write_bytes(cairosvg.svg2png(bytestring=svg.encode(), output_width=W, output_height=H))
print("sheet ok")
