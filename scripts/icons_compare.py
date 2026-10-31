#!/usr/bin/env python3
"""Compare a regenerated branding artifact with the committed one (scripts/icons_check.sh).

Usage: icons_compare.py GENERATED COMMITTED

Byte-identical files pass. A PNG, or an .icns or .ico whose entries are PNGs, also passes
when every image has the same size, the same entry types in the same order, and no channel
of any pixel differs by more than TOLERANCE. cairo rasterises large gradients through
pixman, whose SIMD paths round differently from one CPU to another, so the same source
renders a few levels apart on a different runner; a source edit moves far more than that.
Anything else is compared byte for byte. The largest difference found is printed either way.
"""
import io
import struct
import sys

from PIL import Image

TOLERANCE = 2
PNG = b"\x89PNG\r\n\x1a\n"


def images(data: bytes):
    """The (label, PNG bytes) entries of a file, or None when it is not made of PNGs."""
    if data.startswith(PNG):
        return [("png", data)]
    if data[:4] == b"icns":
        out, at = [], 8
        while at < len(data):
            kind, size = data[at:at + 4], struct.unpack(">I", data[at + 4:at + 8])[0]
            out.append((kind.decode("latin-1"), data[at + 8:at + size]))
            at += size
        return out
    if data[:4] == b"\x00\x00\x01\x00":
        count = struct.unpack("<H", data[4:6])[0]
        out = []
        for i in range(count):
            entry = data[6 + 16 * i:22 + 16 * i]
            size, offset = struct.unpack("<II", entry[8:16])
            out.append((f"ico{entry[0]}", data[offset:offset + size]))
        return out
    return None


def delta(a: bytes, b: bytes) -> int:
    """The largest per-channel difference between two PNGs; -1 when they do not compare."""
    if not (a.startswith(PNG) and b.startswith(PNG)):
        return 0 if a == b else -1
    ia, ib = Image.open(io.BytesIO(a)), Image.open(io.BytesIO(b))
    if ia.size != ib.size or ia.mode != ib.mode:
        return -1
    ia, ib = ia.convert("RGBA"), ib.convert("RGBA")
    return max(abs(x - y) for pa, pb in zip(ia.getdata(), ib.getdata()) for x, y in zip(pa, pb))


def main() -> int:
    generated, committed = (open(p, "rb").read() for p in sys.argv[1:3])
    if generated == committed:
        return 0
    ga, ca = images(generated), images(committed)
    if ga is None or ca is None or [k for k, _ in ga] != [k for k, _ in ca]:
        print("differs byte for byte and is not a comparable image")
        return 1
    worst = max(delta(g, c) for (_, g), (_, c) in zip(ga, ca))
    if worst < 0:
        print("an image differs in size, mode or encoding")
        return 1
    print(f"pixels differ by at most {worst} (tolerance {TOLERANCE})")
    return 0 if worst <= TOLERANCE else 1


if __name__ == "__main__":
    sys.exit(main())
