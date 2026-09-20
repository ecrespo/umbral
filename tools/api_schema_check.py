#!/usr/bin/env python3
"""Compare the protocol the daemon was built with against the API Specification.

REQ-API-004 makes this comparison blocking for method names, required parameters and error
codes, and non-blocking for descriptions and added optional fields. That split is the whole
design: a method or an error code that exists in one place and not the other is drift a
client will hit, while a reworded sentence is not.

The schema comes from the binary (`umb api schema --json`), which generates it from the Go
types by reflection, so this compares two independent derivations of the same contract
rather than a document against a copy of itself.

Usage:
    python3 tools/api_schema_check.py             # runs `go run ./cmd/umb api schema --json`
    python3 tools/api_schema_check.py schema.json # or reads a schema written earlier
"""

from __future__ import annotations

import json
import pathlib
import re
import subprocess
import sys

ROOT = pathlib.Path(__file__).resolve().parent.parent
SPEC = ROOT / "specs" / "api" / "umbral-daemon-api-v1.md"

# Methods the specification documents but no build serves yet. Being in the spec and not in
# the code is not drift — it is the backlog — so it is reported and does not fail.
NOT_BLOCKING_MISSING_FROM_CODE = True


def load_schema(argv: list[str]) -> dict:
    if len(argv) > 1:
        return json.loads(pathlib.Path(argv[1]).read_text())
    out = subprocess.run(
        ["go", "run", "./cmd/umb", "api", "schema", "--json"],
        cwd=ROOT, capture_output=True, text=True, check=False,
    )
    if out.returncode != 0:
        sys.exit(f"api-schema: `umb api schema --json` failed:\n{out.stderr}")
    return json.loads(out.stdout)


def split_top_level(body: str) -> list[str]:
    """Split on commas that are not inside braces, brackets or quotes."""
    parts, depth, quoted, current = [], 0, False, ""
    for ch in body:
        if ch == '"':
            quoted = not quoted
        if not quoted:
            if ch in "{[(":
                depth += 1
            elif ch in "}])":
                depth -= 1
            elif ch == "," and depth == 0:
                parts.append(current)
                current = ""
                continue
        current += ch
    if current.strip():
        parts.append(current)
    return [p.strip() for p in parts if p.strip()]


def parse_inline_params(text: str) -> tuple[set[str], set[str]]:
    """Read `{a, b?, c?: 1, d | e}` into (required, optional)."""
    text = text.strip()
    if not text.startswith("{"):
        return set(), set()
    required, optional = set(), set()

    for item in split_top_level(text[1:-1]):
        # The annotation goes first: `include:"none"|"plain"|"raw"` is one member whose
        # value is one of three, not three members. Splitting on `|` before the `:` reads
        # it as the second and gets `include` wrong.
        name_part = item.split(":")[0].strip()

        alternatives = [a.strip() for a in name_part.split("|")]
        literals = [a for a in alternatives if a.startswith('"')]
        names = [a for a in alternatives if not a.startswith('"')]

        # Two spellings that look alike and are not. `session_id | block_id` (§5.14) means
        # the caller supplies one of the two, so neither is required by itself.
        # `block_id | "last"` (§5.13) means `block_id` takes an id or that literal — the
        # member is required and it is its *value* that varies.
        one_of = len(names) > 1 and not literals

        for alt in names:
            # A parenthetical is a rule on the member, not part of its name:
            # `query (FTS5, 1-256 chars)` is one required member called `query`.
            alt = re.sub(r"\(.*\)", "", alt).strip()
            is_optional = alt.endswith("?")
            alt = alt.rstrip("?").strip().strip("`")
            if not alt or not re.fullmatch(r"\w+", alt):
                continue
            if is_optional or one_of:
                optional.add(alt)
            else:
                required.add(alt)
    return required, optional


def parse_table_params(lines: list[str]) -> tuple[set[str], set[str]]:
    """Read a `| Field | Type | Rule |` block. Optional is whatever the Rule column says."""
    required, optional = set(), set()
    for line in lines:
        if not line.startswith("|") or set(line.strip()) <= set("|- "):
            continue
        cells = [c.strip() for c in line.strip().strip("|").split("|")]
        if len(cells) < 2 or cells[0].lower() == "field":
            continue
        rule = " ".join(cells[2:]).lower() if len(cells) > 2 else ""
        is_optional = "optional" in rule or "default" in rule
        # One cell may name several fields: `` `cols`, `rows` ``.
        for name in re.findall(r"`(\w+)`", cells[0]):
            (optional if is_optional else required).add(name)
    return required, optional


def parse_spec() -> tuple[dict[str, dict], dict[str, int], set[str]]:
    text = SPEC.read_text()
    lines = text.splitlines()

    methods: dict[str, dict] = {}
    heading = re.compile(r"^### 5\.\d+\s+(.*)$")

    i = 0
    while i < len(lines):
        m = heading.match(lines[i])
        if not m:
            i += 1
            continue

        names = re.findall(r"`([\w.]+)`", m.group(1).split("—")[0])
        if not names:
            i += 1
            continue
        namespace = names[0].split(".")[0]
        # `### 5.4 `workspace.create` / `list` / `focus`` names five methods in one heading.
        full = [names[0]] + [f"{namespace}.{n}" for n in names[1:] if "." not in n]

        body, j = [], i + 1
        while j < len(lines) and not lines[j].startswith("###") and not lines[j].startswith("## "):
            body.append(lines[j])
            j += 1

        for name in full:
            methods[name] = {"required": None, "optional": None}

        # Form one: `**Params:** `{...}`` or `**Params:**` above a Field/Type/Rule table.
        # It belongs to the heading's first method.
        for k, line in enumerate(body):
            if not line.startswith("**Params:**"):
                continue
            rest = line[len("**Params:**"):].strip()
            inline = re.search(r"`(\{.*?\})`", rest)
            if inline:
                found = parse_inline_params(inline.group(1))
            else:
                found = parse_table_params(body[k:])
            methods[full[0]] = {"required": found[0], "optional": found[1]}
            break

        # Form two: a heading covering several methods documents them one per line, as
        # ``create` params: `{...}``. §5.4, §5.5 and §5.6 are all written this way, and a
        # parser that only knew form one compared none of the seventeen tree methods.
        for line in body:
            m2 = re.match(r"^`(\w+)`\s+params:\s*`(\{.*?\})`", line.strip())
            if not m2:
                continue
            verb, shape = m2.group(1), m2.group(2)
            target = verb if "." in verb else f"{namespace}.{verb}"
            if target in methods:
                found = parse_inline_params(shape)
                methods[target] = {"required": found[0], "optional": found[1]}
        i = j

    # §3's error table.
    errors: dict[str, int] = {}
    for line in lines:
        m = re.match(r"^\|\s*(-?\d+)\s*\|\s*`(\w+)`\s*\|", line)
        if m:
            errors[m.group(2)] = int(m.group(1))

    # §6's notification table.
    notifications: set[str] = set()
    in_six = False
    for line in lines:
        if line.startswith("## 6."):
            in_six = True
            continue
        if in_six and line.startswith("## "):
            break
        if in_six:
            m = re.match(r"^\|\s*(`[\w.]+`(?:\s*/\s*`\.\w+`)*)\s*\|", line)
            if not m:
                continue
            names = re.findall(r"`([\w.]+)`", m.group(1))
            base = names[0]
            notifications.add(base)
            # `| `tab.created` / `.closed` / `.focused` |` is three rows written as one.
            namespace = base.split(".")[0]
            for suffix in names[1:]:
                notifications.add(namespace + suffix)

    return methods, errors, notifications


def main() -> int:
    schema = load_schema(sys.argv)
    spec_methods, spec_errors, spec_notifications = parse_spec()

    blocking: list[str] = []
    info: list[str] = []

    # --- methods -----------------------------------------------------------------
    code_methods = {m["name"]: m for m in schema["methods"]}
    for name in sorted(code_methods):
        if name not in spec_methods:
            blocking.append(
                f"method `{name}` is served by the daemon and is in no §5 section of the "
                f"specification"
            )

    for name in sorted(spec_methods):
        if name not in code_methods:
            info.append(f"method `{name}` is specified and not served yet")

    # --- required parameters -----------------------------------------------------
    for name, entry in sorted(code_methods.items()):
        spec_entry = spec_methods.get(name)
        if spec_entry is None:
            continue
        if spec_entry["required"] is None:
            # Not drift, but not checked either. Saying so is the difference between "these
            # agree" and "nobody looked": §5.1 defers the handshake's parameters to §2, and
            # several methods take none worth a line.
            info.append(f"`{name}`: §5 documents no parameters; not compared")
            continue
        code_required = set(entry["params"].get("required") or [])
        spec_required = spec_entry["required"]
        spec_optional = spec_entry["optional"]

        for missing in sorted(spec_required - code_required):
            blocking.append(
                f"`{name}`: §5 requires `{missing}` and the daemon does not"
            )
        for extra in sorted(code_required - spec_required):
            if extra in spec_optional:
                blocking.append(
                    f"`{name}`: the daemon requires `{extra}`, which §5 writes as optional"
                )
            else:
                blocking.append(
                    f"`{name}`: the daemon requires `{extra}`, which §5 does not mention"
                )

    # --- error codes -------------------------------------------------------------
    code_errors = {e["domain_code"]: e["code"] for e in schema["errors"]}
    for name in sorted(set(code_errors) | set(spec_errors)):
        in_code, in_spec = code_errors.get(name), spec_errors.get(name)
        if in_code is None:
            blocking.append(f"error `{name}` ({in_spec}) is in §3 and not in the daemon")
        elif in_spec is None:
            blocking.append(f"error `{name}` ({in_code}) is in the daemon and not in §3")
        elif in_code != in_spec:
            blocking.append(
                f"error `{name}` is {in_code} in the daemon and {in_spec} in §3"
            )

    # --- notifications -----------------------------------------------------------
    code_notifications = {n["method"] for n in schema["notifications"]}
    for name in sorted(code_notifications - spec_notifications):
        blocking.append(f"notification `{name}` is emitted and is in no §6 row")
    for name in sorted(spec_notifications - code_notifications):
        info.append(f"notification `{name}` is specified and not emitted yet")

    # --- report ------------------------------------------------------------------
    print(
        f"api-schema: {len(code_methods)} methods, {len(code_notifications)} notifications, "
        f"{len(code_errors)} error codes in the binary; "
        f"{len(spec_methods)} methods, {len(spec_notifications)} notifications, "
        f"{len(spec_errors)} error codes in the specification"
    )
    for line in info:
        print(f"  info: {line}")

    if blocking:
        print(file=sys.stderr)
        for line in blocking:
            print(f"api-schema: {line}", file=sys.stderr)
        print(
            f"\napi-schema: {len(blocking)} blocking difference(s). REQ-API-004 makes method "
            f"names, required parameters and error codes binding; a change to either side "
            f"needs the other, and a spec change needs a Delta.",
            file=sys.stderr,
        )
        return 1

    print("api-schema: the binary and the specification agree")
    return 0


if __name__ == "__main__":
    sys.exit(main())
