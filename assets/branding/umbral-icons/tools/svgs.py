"""Umbral icon sources. Every variant shares one identity:
an arched doorway (the threshold) lit from inside, a prompt `>_` standing in the light,
and the sill (the "umbral", Spanish for threshold) where the light spills out.
"""

# Palette ---------------------------------------------------------------
INDIGO_950 = "#100D2E"
INDIGO_900 = "#16123D"
INDIGO_800 = "#1E1A52"
INDIGO_700 = "#2B2670"
INDIGO_600 = "#3A34A0"
INDIGO_400 = "#6A63D0"
STONE_TOP = "#5A53B8"
AMBER_100 = "#FFF6E0"
AMBER_300 = "#FFD27A"
AMBER_400 = "#FFB443"
AMBER_600 = "#EE861A"
SYMBOLIC = "#241F31"


def _svg(w, h, body, defs=""):
    return (
        f'<svg xmlns="http://www.w3.org/2000/svg" width="{w}" height="{h}" '
        f'viewBox="0 0 {w} {h}">\n<defs>{defs}</defs>\n{body}\n</svg>\n'
    )


def _light_grad(gid, cx, cy, r):
    return (
        f'<radialGradient id="{gid}" gradientUnits="userSpaceOnUse" cx="{cx}" cy="{cy}" r="{r}">'
        f'<stop offset="0" stop-color="{AMBER_100}"/>'
        f'<stop offset="0.42" stop-color="{AMBER_300}"/>'
        f'<stop offset="0.75" stop-color="{AMBER_400}"/>'
        f'<stop offset="1" stop-color="{AMBER_600}"/></radialGradient>'
    )


# 1. Object icon, 128 grid (Linux hicolor scalable + Windows, >= 32 px) ----
def object_128():
    defs = (
        _light_grad("light", 64, 108, 84)
        + f'<linearGradient id="frame" x1="0" y1="14" x2="0" y2="108" gradientUnits="userSpaceOnUse">'
        f'<stop offset="0" stop-color="{INDIGO_600}"/><stop offset="1" stop-color="{INDIGO_800}"/></linearGradient>'
        f'<linearGradient id="spill" x1="0" y1="106" x2="0" y2="114" gradientUnits="userSpaceOnUse">'
        f'<stop offset="0" stop-color="{AMBER_300}"/><stop offset="1" stop-color="{AMBER_400}" stop-opacity="0.85"/></linearGradient>'
    )
    body = f"""
<!-- sill: front face, then top face -->
<rect x="16" y="110" width="96" height="10" rx="3" fill="{INDIGO_900}"/>
<rect x="16" y="106" width="96" height="8" rx="3" fill="{STONE_TOP}"/>
<!-- light spilling over the sill -->
<path d="M37 106 H91 L100 114 H28 Z" fill="url(#spill)"/>
<!-- door frame -->
<path d="M24 107 V54 A40 40 0 0 1 104 54 V107 Z" fill="url(#frame)"/>
<path d="M25.5 54 A38.5 38.5 0 0 1 102.5 54" fill="none" stroke="{INDIGO_400}" stroke-width="3" stroke-opacity="0.6"/>
<!-- lit opening -->
<path d="M37 107 V56 A27 27 0 0 1 91 56 V107 Z" fill="url(#light)"/>
<!-- prompt >_ -->
<path d="M47 68 L59 80 L47 92" fill="none" stroke="{INDIGO_800}" stroke-width="8"
      stroke-linecap="round" stroke-linejoin="round"/>
<rect x="64" y="86" width="18" height="7" rx="2" fill="{INDIGO_800}"/>
"""
    return _svg(128, 128, body, defs)


# 2. Hinted 24 px object (also used for 20/22 px) ------------------------
def object_24():
    defs = _light_grad("light", 12, 20, 15)
    body = f"""
<rect x="2" y="20" width="20" height="3" rx="1" fill="{INDIGO_900}"/>
<rect x="2" y="19" width="20" height="2" rx="1" fill="{STONE_TOP}"/>
<path d="M4 19 V10 A8 8 0 0 1 20 10 V19 Z" fill="{INDIGO_700}"/>
<path d="M6.5 19 V10.5 A5.5 5.5 0 0 1 17.5 10.5 V19 Z" fill="url(#light)"/>
<path d="M8.5 11.5 L11 14 L8.5 16.5" fill="none" stroke="{INDIGO_800}" stroke-width="2"
      stroke-linecap="round" stroke-linejoin="round"/>
<rect x="12.5" y="15.2" width="3.5" height="1.8" rx="0.5" fill="{INDIGO_800}"/>
"""
    return _svg(24, 24, body, defs)


# 3. Hinted 16 px object ----------------------------------------------------
def object_16():
    defs = _light_grad("light", 8, 14, 10)
    body = f"""
<rect x="1" y="13" width="14" height="3" rx="1" fill="{STONE_TOP}"/>
<path d="M2 13 V7 A6 6 0 0 1 14 7 V13 Z" fill="{INDIGO_700}"/>
<path d="M4 13 V7.5 A4 4 0 0 1 12 7.5 V13 Z" fill="url(#light)"/>
<path d="M5.5 7.8 L7.5 9.8 L5.5 11.8" fill="none" stroke="{INDIGO_800}" stroke-width="1.5"
      stroke-linecap="round" stroke-linejoin="round"/>
<rect x="8.2" y="11" width="2.6" height="1.3" fill="{INDIGO_800}"/>
"""
    return _svg(16, 16, body, defs)


# 4. GNOME symbolic, 16 px, fills only (recolorable) ------------------------
def symbolic_16():
    body = f"""
<g fill="{SYMBOLIC}">
  <path fill-rule="evenodd" d="M2 13 V7 A6 6 0 0 1 14 7 V13 Z M4 13 V7 A4 4 0 0 1 12 7 V13 Z"/>
  <rect x="1" y="14" width="14" height="2" rx="0.5"/>
  <path d="M5.293 7.707 L6.707 6.293 L9.414 9 L6.707 11.707 L5.293 10.293 L6.586 9 Z"/>
</g>
"""
    return _svg(16, 16, body)


# 5. KDE Breeze-style variant, 48 px baseline --------------------------------
def breeze_48():
    defs = (
        _light_grad("light", 24, 38, 24)
        + f'<linearGradient id="base" x1="0" y1="4" x2="0" y2="44" gradientUnits="userSpaceOnUse">'
        f'<stop offset="0" stop-color="{INDIGO_600}"/><stop offset="1" stop-color="{INDIGO_800}"/></linearGradient>'
        '<clipPath id="baseclip"><rect x="4" y="4" width="40" height="40" rx="3"/></clipPath>'
    )
    body = f"""
<rect x="4" y="5" width="40" height="40" rx="3" fill="{INDIGO_950}" opacity="0.6"/>
<rect x="4" y="4" width="40" height="40" rx="3" fill="url(#base)"/>
<g clip-path="url(#baseclip)">
  <!-- 45 degree long shadow of the foreground -->
  <path d="M33 19 L63 49 L45 67 L15 38 Z" fill="#000" opacity="0.18"/>
  <!-- sill -->
  <rect x="9" y="38" width="30" height="3" rx="1" fill="{STONE_TOP}"/>
</g>
<path d="M15 38 V19 A9 9 0 0 1 33 19 V38 Z" fill="url(#light)"/>
<path d="M19.5 22.5 L23.5 26.5 L19.5 30.5" fill="none" stroke="{INDIGO_800}" stroke-width="2.5"
      stroke-linecap="round" stroke-linejoin="round"/>
<rect x="25" y="29" width="5.5" height="2.2" rx="0.6" fill="{INDIGO_800}"/>
"""
    return _svg(48, 48, body, defs)


# 6. macOS layers (1024, full-bleed, NO baked mask) ------------------------------
def mac_background():
    defs = (
        '<linearGradient id="bg" x1="0" y1="0" x2="0" y2="1024" gradientUnits="userSpaceOnUse">'
        f'<stop offset="0" stop-color="#2A2575"/><stop offset="1" stop-color="{INDIGO_950}"/></linearGradient>'
    )
    return _svg(1024, 1024, '<rect width="1024" height="1024" fill="url(#bg)"/>', defs)


def mac_spill():
    defs = (
        '<linearGradient id="spill" x1="0" y1="820" x2="0" y2="1024" gradientUnits="userSpaceOnUse">'
        f'<stop offset="0" stop-color="{INDIGO_400}" stop-opacity="0.95"/>'
        f'<stop offset="1" stop-color="{INDIGO_600}" stop-opacity="0"/></linearGradient>'
    )
    body = f"""
<path d="M312 820 H712 L840 1024 H184 Z" fill="url(#spill)"/>
<rect x="250" y="800" width="524" height="40" rx="14" fill="{AMBER_300}"/>
"""
    return _svg(1024, 1024, body, defs)


def mac_door():
    defs = _light_grad("light", 512, 800, 520)
    return _svg(1024, 1024, '<path d="M312 800 V440 A200 200 0 0 1 712 440 V800 Z" fill="url(#light)"/>', defs)


def mac_prompt():
    body = f"""
<path d="M398 520 L494 616 L398 712" fill="none" stroke="{INDIGO_800}" stroke-width="60"
      stroke-linecap="round" stroke-linejoin="round"/>
<rect x="530" y="668" width="140" height="52" rx="16" fill="{INDIGO_800}"/>
"""
    return _svg(1024, 1024, body)


def _inner(svg_text):
    """Strip the outer <svg> wrapper, keep defs+body (ids are unique per layer)."""
    start = svg_text.index(">", svg_text.index("<svg")) + 1
    end = svg_text.rindex("</svg>")
    return svg_text[start:end]


def mac_flat_tile():
    """All layers composited, full-bleed square (input for Wails/actool previews)."""
    body = "".join(_inner(f()) for f in (mac_background, mac_spill, mac_door, mac_prompt))
    return f'<svg xmlns="http://www.w3.org/2000/svg" width="1024" height="1024" viewBox="0 0 1024 1024">{body}</svg>\n'


def mac_legacy_icns():
    """Pre-Tahoe style: 824 px rounded tile centered on 1024 canvas with soft shadow."""
    inner = "".join(_inner(f()) for f in (mac_background, mac_spill, mac_door, mac_prompt))
    return f"""<svg xmlns="http://www.w3.org/2000/svg" width="1024" height="1024" viewBox="0 0 1024 1024">
<defs>
  <clipPath id="tile"><rect x="0" y="0" width="1024" height="1024" rx="230"/></clipPath>
  <filter id="shadow" x="-10%" y="-10%" width="120%" height="130%">
    <feGaussianBlur in="SourceAlpha" stdDeviation="14"/><feOffset dy="12"/>
    <feComponentTransfer><feFuncA type="linear" slope="0.35"/></feComponentTransfer>
    <feMerge><feMergeNode/><feMergeNode in="SourceGraphic"/></feMerge>
  </filter>
</defs>
<g transform="translate(100 100) scale(0.8047)">
  <rect width="1024" height="1024" rx="230" fill="#000" filter="url(#shadow)"/>
  <g clip-path="url(#tile)">{inner}</g>
</g>
</svg>
"""


# 7. Concept B (exploration only): "Penumbra" ------------------------------------
def concept_penumbra():
    body = f"""
<circle cx="64" cy="64" r="48" fill="{INDIGO_800}"/>
<path d="M64 16 A48 48 0 0 1 64 112 Z" fill="{AMBER_400}"/>
<rect x="60" y="30" width="8" height="68" rx="3" fill="{AMBER_100}"/>
<path d="M36 52 L48 64 L36 76" fill="none" stroke="{AMBER_300}" stroke-width="7"
      stroke-linecap="round" stroke-linejoin="round"/>
"""
    return _svg(128, 128, body)
