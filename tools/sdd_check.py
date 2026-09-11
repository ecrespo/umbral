#!/usr/bin/env python3
"""Read-only SDD coverage check for Umbral (feeds the Analyze report and CI).

Checks: MUST coverage in tasks + matrices, ghost REQs, tasks without REQ,
matrix tests citing their REQ, and SQL DDL executability.
Exit code 1 if any CRITICAL finding.
"""
import re
import sqlite3
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
REQ_DEF = re.compile(r"\*\*(REQ-[A-Z]+-\d{3})\*\* · (MUST|SHOULD|COULD|WONT)")
REQ_REF = re.compile(r"REQ-[A-Z]+-\d{3}")
ART_REF = re.compile(r"Art\. ?\d+")


def defined(paths):
    reqs = {}
    for p in paths:
        for rid, prio in REQ_DEF.findall(p.read_text(encoding="utf-8")):
            reqs[rid] = (prio, p.relative_to(ROOT).as_posix())
    return reqs


def tasks(path):
    text = path.read_text(encoding="utf-8")
    out = {}
    for block in re.split(r"\n### ", text)[1:]:
        m = re.match(r"\[.\] (T-[A-Z0-9]+-\d{2})", block)
        if not m:
            continue
        req_line = re.search(r"\*\*REQ:\*\* (.*)", block)
        raw = req_line.group(1) if req_line else ""
        reqs = REQ_REF.findall(raw)
        # Infrastructure tasks may cite a constitution article instead of a functional REQ
        # (documented exception in the tasks files' conventions section).
        out[m.group(1)] = reqs if reqs else (["Art."] if ART_REF.search(raw) else [])
    matrix = {}
    if "## Traceability matrix" in text:
        sect = text.split("## Traceability matrix", 1)[1]
        for row in re.findall(r"^\| (REQ-[A-Z]+-\d{3}) \| ([^|]*)\| ([^|]*)\|", sect, re.M):
            matrix[row[0]] = (row[1].strip(), row[2].strip())
    return out, matrix


def main():
    base = defined([ROOT / "specs/prd/umbral-mvp.md"])
    delta_files = sorted(p for p in (ROOT / "changes").glob("*/delta-spec.md") if "_archive" not in p.parts)
    delta = defined(delta_files)
    all_defined = {**base, **delta}
    findings = []

    task_files = sorted((ROOT / "specs/tasks").glob("*.md")) + [p.with_name("tasks.md") for p in delta_files if p.with_name("tasks.md").exists()]
    cited, matrix, orphan, by_article = {}, {}, [], []
    for tf in task_files:
        t, m = tasks(tf)
        for tid, reqs in t.items():
            if not reqs:
                orphan.append((tid, tf.name))
            elif reqs == ["Art."]:
                by_article.append((tid, tf.name))
                continue
            for r in reqs:
                cited.setdefault(r, []).append(tid)
        matrix.update(m)

    for rid, (prio, src) in sorted(all_defined.items()):
        if prio == "MUST" and rid not in cited:
            findings.append(("CRITICAL", f"{rid} (MUST) has no task", src))
        if prio == "MUST" and rid not in matrix:
            findings.append(("HIGH", f"{rid} (MUST) missing from the traceability matrix", src))
    for rid in sorted(set(cited) | set(matrix)):
        if rid not in all_defined:
            findings.append(("CRITICAL", f"{rid} cited but not defined (ghost REQ)", "tasks"))
    for rid, (_, tests) in sorted(matrix.items()):
        suffix = rid.replace("-", "_")
        if rid.startswith("REQ-PKG"):
            continue  # packaging verifications are commands, not Go tests
        if tests and suffix not in tests and "e2e" not in tests and "deferred" not in tests.lower():
            findings.append(("MEDIUM", f"{rid}: no test in the matrix cites the ID in its name ({tests[:60]})", "tasks"))
    for tid, tf in orphan:
        findings.append(("LOW", f"{tid} has no REQ and no constitution Art. cited", tf))

    # SQL DDL executability
    dm = (ROOT / "specs/data-model/umbral-schema.md").read_text(encoding="utf-8")
    ddl = "\n".join(re.findall(r"```sql\n(.*?)```", dm, re.S))
    con = sqlite3.connect(":memory:")
    try:
        con.executescript("PRAGMA foreign_keys=ON;\n" + ddl)
        tables = [r[0] for r in con.execute("select name from sqlite_master where type in ('table') order by 1")]
        sql_ok = f"DDL OK ({len(tables)} tables/objects)"
    except sqlite3.Error as e:
        sql_ok = f"DDL ERROR: {e}"
        findings.append(("CRITICAL", sql_ok, "data-model"))

    # Migration 0001 (F0) alone: objects planned in T-F0-02 must accept inserts with FKs on.
    # `threads` belongs to 0001 (Data Model §5.1) because sessions/blocks hold FKs against it.
    f0_tables = ("threads", "sessions", "blocks", "block_chunks", "blocks_fts")
    f0_ddl = [b for b in re.findall(r"```sql\n(.*?)```", dm, re.S)
              if re.search(r"CREATE (VIRTUAL )?TABLE (%s)\b" % "|".join(f0_tables), b)
              or re.search(r"CREATE TRIGGER blocks_fts_", b)]
    con0 = sqlite3.connect(":memory:")
    try:
        con0.executescript("PRAGMA foreign_keys=ON;\n" + "\n".join(f0_ddl))
        con0.execute("INSERT INTO sessions(id,shell,cwd,cols,rows,state,created_at) "
                     "VALUES ('ses_x','/bin/sh','/',80,24,'alive',0)")
        con0.execute("INSERT INTO blocks(id,session_id,origin,command,state,started_at,output_plain) "
                     "VALUES ('blk_x','ses_x','user','go test ./...','finished',0,'ok 1 passed')")
        hits = con0.execute("SELECT count(*) FROM blocks_fts WHERE blocks_fts MATCH 'passed'").fetchone()[0]
        if hits != 1:
            findings.append(("CRITICAL", f"blocks_fts triggers do not index new blocks (MATCH hits={hits})", "data-model"))
        sql_ok += " · migration 0001 in isolation OK (INSERT + FTS MATCH)"
    except sqlite3.Error as e:
        findings.append(("CRITICAL", f"migration 0001 in isolation rejects INSERT into sessions: {e}", "data-model"))

    must = [r for r, (p, _) in all_defined.items() if p == "MUST"]
    print(f"REQs defined: {len(all_defined)} (MUST {len(must)}) · with task: {sum(1 for r in must if r in cited)}/{len(must)}")
    print(f"Tasks: {sum(len(tasks(tf)[0]) for tf in task_files)} · "
          f"infrastructure tasks citing a constitution Art.: {len(by_article)} "
          f"({', '.join(t for t, _ in by_article) or 'none'}) · uncited: {len(orphan)}")
    print(sql_ok)
    for sev, msg, src in findings:
        print(f"[{sev}] {msg} — {src}")
    sys.exit(1 if any(f[0] == "CRITICAL" for f in findings) else 0)


if __name__ == "__main__":
    main()
