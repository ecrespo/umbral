// The Umbral desktop window. It keeps no state the daemon does not have: the tree is
// `session.snapshot` read again on every tree notification, a terminal is a frame the bridge
// drew from the daemon's stream, and every action is a protocol method (REQ-DSK-001).
// Everything a program or a model wrote is placed with textContent, never as markup.

const call = (method, params) => wails.Call.ByName("main.Umbral.Call", method, params ?? {});
const bridge = (name, ...args) => wails.Call.ByName("main.Umbral." + name, ...args);

const state = {
  snap: null,          // the last session.snapshot
  focusedTab: null,    // the tab shown
  focusedPane: null,   // the pane that receives keys
  attached: new Map(), // session_id → {el, cols, rows}
  view: "threads",
  thread: null,        // the thread shown in the threads view
  transcript: new Map(), // thread_id → [{role, text}]
  smoke: "",
  home: "",            // where a new workspace starts when no pane is focused
};

const $ = (sel) => document.querySelector(sel);
const el = (tag, cls, text) => {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
};
const button = (label, onClick, cls) => {
  const b = el("button", cls, label);
  b.addEventListener("click", (ev) => { ev.stopPropagation(); onClick(); });
  return b;
};
const status = (text, error) => { const s = $("#status"); s.textContent = text; s.className = error ? "error" : ""; };
const failed = (err) => status(String(err?.message ?? err), true);
// notice shows a daemon notification in the status line until the next one, as text.
const notice = (p) => {
  const s = $("#status");
  s.textContent = [p?.title, p?.body].filter(Boolean).join(": ");
  s.className = "notice";
};
const newID = () => {
  // A ULID for thread.send's client_msg_id: 48 bits of time, 80 of randomness, Crockford base32.
  const A = "0123456789ABCDEFGHJKMNPQRSTVWXYZ";
  let t = Date.now(), time = "";
  for (let i = 0; i < 10; i++) { time = A[t % 32] + time; t = Math.floor(t / 32); }
  const r = crypto.getRandomValues(new Uint8Array(16));
  let rand = "";
  for (let i = 0; i < 16; i++) rand += A[r[i] % 32];
  return time + rand;
};

// --- the tree ---------------------------------------------------------------------------

// A refresh asked while one runs is not lost: the running one reads the snapshot once more
// when it ends, so the last change is always drawn.
let refreshing = null, again = false;
async function refresh() {
  if (refreshing) { again = true; return refreshing; }
  refreshing = (async () => {
    do {
      again = false;
      try {
        state.snap = await call("session.snapshot");
        const f = state.snap.focused ?? {};
        const tabs = state.snap.tabs ?? [];
        if (!tabs.some((t) => t.id === state.focusedTab)) state.focusedTab = f.tab_id ?? tabs[0]?.id ?? null;
        renderTree();
        renderPanes();
        if (state.view === "threads" && !state.thread) renderView();
      } catch (err) { failed(err); }
    } while (again);
    refreshing = null;
  })();
  return refreshing;
}

function renderTree() {
  const nav = $("#tree");
  nav.replaceChildren();
  for (const ws of state.snap.workspaces ?? []) {
    if (ws.closed_at) continue;
    const box = el("div", "ws");
    const name = el("div", "name", ws.label || ws.id);
    name.addEventListener("click", () => call("workspace.focus", { workspace_id: ws.id }).catch(failed));
    box.append(name);
    for (const tab of (state.snap.tabs ?? []).filter((t) => t.workspace_id === ws.id && !t.closed_at)) {
      const t = el("div", "tab" + (tab.id === state.focusedTab ? " focused" : ""), tab.label || tab.id);
      t.addEventListener("click", () => { state.focusedTab = tab.id; renderTree(); renderPanes(); call("tab.focus", { tab_id: tab.id }).catch(failed); });
      box.append(t);
    }
    const actions = el("div", "tree-actions");
    actions.append(button("+ tab", () => call("tab.create", { workspace_id: ws.id }).then(refresh).catch(failed)));
    box.append(actions);
    nav.append(box);
  }
  const actions = el("div", "tree-actions");
  actions.append(button("+ workspace", async () => {
    const cwd = state.snap.panes?.find((p) => p.id === state.focusedPane)?.cwd;
    try { await call("workspace.create", { cwd: cwd ?? state.home }); await refresh(); } catch (err) { failed(err); }
  }));
  nav.append(actions);
}

function renderPanes() {
  const host = $("#panes");
  const layout = (state.snap.layouts ?? []).find((l) => l.tab_id === state.focusedTab);
  const panes = new Map((state.snap.panes ?? []).map((p) => [p.id, p]));
  const keep = new Set();
  host.replaceChildren();
  if (!layout) { host.append(el("p", "meta", "No tab. Create a workspace.")); detachAll(keep); return; }
  if (!panes.has(state.focusedPane)) state.focusedPane = layout.focused_pane_id;
  host.append(node(layout.root, panes, keep));
  detachAll(keep);
  requestAnimationFrame(sizeAll);
}

function node(n, panes, keep) {
  if (n.type === "split") {
    const s = el("div", "split " + n.direction);
    const a = node(n.first, panes, keep), b = node(n.second, panes, keep);
    a.style.flex = String(n.ratio ?? 0.5); b.style.flex = String(1 - (n.ratio ?? 0.5));
    s.append(a, b);
    return s;
  }
  const p = panes.get(n.pane_id);
  const box = el("div", "pane" + (n.pane_id === state.focusedPane ? " focused" : ""));
  const head = el("div", "pane-head");
  head.append(el("span", "label", (p?.label || n.pane_id) + (p?.cwd ? "  " + p.cwd : "")));
  if (p?.detected_agent?.name) head.append(el("span", "", p.detected_agent.name));
  if (p?.attention_state && p.attention_state !== "unknown") head.append(el("span", "", p.attention_state));
  head.append(
    button("⇥", () => call("pane.split", { pane_id: n.pane_id, direction: "right" }).then(refresh).catch(failed)),
    button("⇣", () => call("pane.split", { pane_id: n.pane_id, direction: "down" }).then(refresh).catch(failed)),
    button("×", () => call("pane.close", { pane_id: n.pane_id }).then(refresh).catch(failed), "danger"),
  );
  const screen = el("pre", "screen");
  screen.tabIndex = 0;
  screen.dataset.session = p?.session_id ?? "";
  screen.addEventListener("focus", () => focusPane(n.pane_id, box));
  screen.addEventListener("keydown", (ev) => keydown(ev, p?.session_id));
  screen.addEventListener("paste", (ev) => {
    ev.preventDefault();
    const text = ev.clipboardData.getData("text/plain");
    if (p?.session_id && text) bridge("Input", p.session_id, text).catch(failed);
  });
  box.append(head, screen);
  if (p?.session_id) {
    keep.add(p.session_id);
    const known = state.attached.get(p.session_id);
    if (known) { known.el = screen; if (known.frame) draw(screen, known.frame); }
    else state.attached.set(p.session_id, { el: screen, cols: 0, rows: 0 });
  }
  return box;
}

function focusPane(id, box) {
  state.focusedPane = id;
  document.querySelectorAll(".pane.focused").forEach((e) => e.classList.remove("focused"));
  box.classList.add("focused");
  call("pane.focus", { pane_id: id }).catch(() => {});
}

function detachAll(keep) {
  for (const id of [...state.attached.keys()]) {
    if (!keep.has(id)) { state.attached.delete(id); bridge("Detach", id).catch(() => {}); }
  }
}

// --- terminals --------------------------------------------------------------------------

let cell = null;
function cellSize() {
  if (cell) return cell;
  const m = el("span", "", "M".repeat(100));
  m.id = "measure";
  document.body.append(m);
  const r = m.getBoundingClientRect();
  m.remove();
  cell = { w: r.width / 100, h: r.height };
  return cell;
}

function sizeAll() {
  const c = cellSize();
  for (const [id, t] of state.attached) {
    const r = t.el.getBoundingClientRect();
    const cols = Math.max(20, Math.floor((r.width - 8) / c.w));
    const rows = Math.max(5, Math.floor((r.height - 4) / c.h));
    if (cols === t.cols && rows === t.rows) continue;
    const first = t.cols === 0;
    t.cols = cols; t.rows = rows;
    // A session's attach and resizes run one after another: a resize sent while the attach
    // is in flight would resize the daemon's terminal and not the screen being built.
    t.queue = (t.queue ?? Promise.resolve()).then(() => (first ? bridge("Attach", id, cols, rows) : bridge("Resize", id, cols, rows))).then(() => {
      // The smoke types arithmetic the shell must evaluate, so the echo of the typed line
      // cannot pass for the command's output.
      if (first && state.smoke && !t.smoked) { t.smoked = true; bridge("Input", id, `echo smoke-$((${state.smoke}+1))\r`); }
    }).catch(failed);
  }
}
new ResizeObserver(() => requestAnimationFrame(sizeAll)).observe(document.body);

function draw(screen, f) {
  const rows = [];
  f.rows.forEach((runs, y) => {
    const row = el("div", "row");
    let x = 0;
    for (const run of runs ?? []) {
      const chars = [...run.t];
      const style = (span) => {
        if (run.fg) span.style.color = run.fg;
        if (run.bg) span.style.background = run.bg;
        if (run.b) span.style.fontWeight = "bold";
        if (run.f) span.style.opacity = "0.6";
        if (run.i) span.style.fontStyle = "italic";
        const deco = [run.u && "underline", run.s && "line-through"].filter(Boolean).join(" ");
        if (deco) span.style.textDecoration = deco;
      };
      if (f.cursor.visible && f.cursor.y === y && f.cursor.x >= x && f.cursor.x < x + chars.length) {
        const at = f.cursor.x - x;
        for (const [part, cls] of [[chars.slice(0, at), ""], [chars.slice(at, at + 1), "cursor"], [chars.slice(at + 1), ""]]) {
          if (!part.length) continue;
          const span = el("span", cls, part.join(""));
          style(span);
          row.append(span);
        }
      } else {
        const span = el("span", "", run.t);
        style(span);
        row.append(span);
      }
      x += chars.length;
    }
    rows.push(row);
  });
  screen.style.background = f.bg;
  screen.style.color = f.fg;
  screen.replaceChildren(...rows);
}

const keys = {
  Enter: "\r", Backspace: "\x7f", Tab: "\t", Escape: "\x1b",
  ArrowUp: "\x1b[A", ArrowDown: "\x1b[B", ArrowRight: "\x1b[C", ArrowLeft: "\x1b[D",
  Home: "\x1b[H", End: "\x1b[F", Delete: "\x1b[3~", PageUp: "\x1b[5~", PageDown: "\x1b[6~", Insert: "\x1b[2~",
};
function keydown(ev, sessionID) {
  if (!sessionID || ev.metaKey) return;
  // ctrl+shift+C and ctrl+shift+V are the webview's copy and paste; a paste arrives as the
  // paste event below.
  if (ev.ctrlKey && ev.shiftKey && (ev.key === "C" || ev.key === "V")) return;
  let data = keys[ev.key];
  if (ev.ctrlKey && !ev.altKey && ev.key.length === 1) {
    const c = ev.key.toLowerCase().charCodeAt(0);
    if (c >= 97 && c <= 122) data = String.fromCharCode(c - 96);
    else if (ev.key === "[") data = "\x1b";
    else if (ev.key === " ") data = "\x00";
  } else if (!data && ev.key.length === 1 && !ev.ctrlKey) {
    data = (ev.altKey ? "\x1b" : "") + ev.key;
  }
  if (!data) return;
  ev.preventDefault();
  bridge("Input", sessionID, data).catch(failed);
}

// --- agents -----------------------------------------------------------------------------

function renderView() {
  const view = $("#view");
  view.replaceChildren();
  ({ threads: renderThreads, approvals: renderApprovals, worktrees: renderWorktrees, files: renderFiles, blocks: renderBlocks })[state.view](view)
    .catch((err) => view.append(el("p", "error", String(err?.message ?? err))));
}

async function renderThreads(view) {
  if (state.thread) return renderThread(view, state.thread);
  const ws = state.snap?.focused?.workspace_id;
  const create = button("New thread", async () => {
    const cwd = state.snap.panes?.find((p) => p.id === state.focusedPane)?.cwd ?? state.home;
    try {
      const t = await call("thread.create", ws ? { cwd, workspace: ws } : { cwd });
      state.thread = t.id; renderView();
    } catch (err) { failed(err); }
  });
  view.append(create);
  if (!ws) { view.append(el("p", "meta", "No workspace is focused.")); return; }
  // Every thread of the focused workspace and no other, with what REQ-ORC-003 asks to see
  // without opening it: attention, queued, worktree and pending approvals.
  for (const t of await bridge("Threads", ws)) {
    const item = el("div", "item" + (t.pending_approvals ? " blocked" : ""));
    const approvals = t.pending_approvals ? `${t.pending_approvals} approval${t.pending_approvals === 1 ? "" : "s"}` : null;
    item.append(el("div", "title", t.title || t.id), el("div", "meta", [
      t.attention_state, t.queued ? "queued" : null, t.worktree_id || "no worktree", approvals, t.state, t.mode, t.agent,
    ].filter(Boolean).join(" · ")));
    item.addEventListener("click", () => { state.thread = t.id; renderView(); });
    view.append(item);
  }
}

async function renderThread(view, id) {
  // Every page of the transcript, oldest first (API §5.23). Text streamed before this read is
  // in it, so only what arrives from now on is kept beside it.
  state.transcript.delete(id);
  const messages = [];
  let t, cursor = null;
  do {
    t = await call("thread.get", { thread_id: id, include_messages: true, limit: 1000, ...(cursor ? { cursor } : {}) });
    messages.push(...(t.messages ?? []));
    cursor = t.next_cursor ?? null;
  } while (cursor);
  call("thread.focus", { thread_id: id }).catch(() => {});
  const back = button("← threads", () => { leaveThread(); renderView(); });
  view.append(back, el("div", "title", t.title || t.id), el("div", "meta", [t.state, t.attention_state, t.mode, t.model].filter(Boolean).join(" · ")));
  if (t.egress_visibility === "unobserved") view.append(el("p", "meta", "This agent's own traffic and file reads are not recorded by Umbral. The agent can read the repository's main copy and every other thread's worktree."));
  const log = el("div", "transcript");
  for (const m of messages) log.append(el("div", "msg " + m.role, messageText(m)));
  for (const m of state.transcript.get(id) ?? []) log.append(el("div", "msg " + m.role, m.text));
  view.append(log);
  const input = el("textarea");
  input.placeholder = "Message the agent";
  const send = button("Send", async () => {
    const text = input.value.trim();
    if (!text) return;
    try {
      await call("thread.send", { thread_id: id, text, client_msg_id: newID() });
      state.transcript.delete(id);
      input.value = "";
      renderView();
    } catch (err) { failed(err); }
  });
  const actions = el("div", "row-actions");
  actions.append(send, button("Cancel", () => call("thread.cancel", { thread_id: id }).catch(failed), "danger"));
  view.append(input, actions);
}

// leaveThread stops viewing the shown thread, so its turns stop ending `idle` on this
// connection's account (API §5.23).
function leaveThread() {
  if (!state.thread) return;
  call("thread.focus", { thread_id: null }).catch(() => {});
  state.thread = null;
}

function messageText(m) {
  if (typeof m.content === "string") return m.content;
  if (Array.isArray(m.content)) return m.content.map((p) => p.text ?? "").join("");
  return m.text ?? "";
}

async function renderApprovals(view) {
  const { items } = await call("approval.list");
  $("#approval-count").textContent = items.length ? `(${items.length})` : "";
  if (!items.length) view.append(el("p", "meta", "Nothing waits for you."));
  for (const a of items) {
    const item = el("div", "item");
    item.append(el("div", "title", `${a.tool} · ${a.risk}`), el("div", "meta", a.reason ?? ""), el("pre", "diff", a.summary ?? ""));
    // A diff whose hunks may be chosen shows each with a checkbox, all checked; approving with
    // some unchecked sends the checked ids, and the call writes only those (REQ-DSK-002).
    const picks = a.hunks_selectable && a.hunks ? hunkPicker(a.hunks) : null;
    if (picks) item.append(picks.node); else if (a.diff) item.append(diffView(a.diff));
    const actions = el("div", "row-actions");
    const respond = (decision, scope, hunks) => call("approval.respond", { approval_id: a.id, decision, scope, ...(hunks ? { hunks } : {}) }).then(() => renderView()).catch(failed);
    actions.append(button("Approve once", () => {
      const chosen = picks?.checked();
      if (chosen && chosen.length === 0) { status("No hunk is checked: deny the call instead.", true); return; }
      respond("approve", "once", chosen && chosen.length < picks.total ? chosen : undefined);
    }, "ok"));
    // A remembered rule approves whole calls to come, never part of one.
    actions.append(button("Approve for thread", () => {
      if (picks && picks.checked().length < picks.total) { status("A rule approves a whole call: check every hunk, or approve once.", true); return; }
      respond("approve", "thread");
    }), button("Deny", () => respond("deny", "once"), "danger"));
    item.append(actions);
    view.append(item);
  }
}

// hunkPicker draws a diff's files (worktree.diff's `files`, an approval's `hunks`) with a
// checkbox on each hunk, all checked; checked() is the ids still checked.
function hunkPicker(files) {
  const node = el("div");
  const boxes = [];
  for (const f of files) {
    node.append(el("div", "title", `${f.status} ${f.path}`));
    if (!f.hunks?.length) node.append(el("div", "meta", "no hunks to choose: a binary file or a mode change"));
    for (const h of f.hunks ?? []) {
      const box = document.createElement("input");
      box.type = "checkbox";
      box.checked = true;
      box.dataset.hunk = h.id;
      const label = el("label", "hunk");
      label.append(box, diffView([h.header, ...h.lines].join("\n")));
      boxes.push(box);
      node.append(label);
    }
  }
  return { node, total: boxes.length, checked: () => boxes.filter((b) => b.checked).map((b) => b.dataset.hunk), unchecked: () => boxes.filter((b) => !b.checked).map((b) => b.dataset.hunk) };
}

function diffView(text) {
  const pre = el("pre", "diff");
  for (const line of String(text).split("\n")) {
    const cls = line.startsWith("@@") ? "hunkhead" : line.startsWith("+") ? "add" : line.startsWith("-") ? "del" : "";
    pre.append(el("div", cls, line));
  }
  return pre;
}

async function renderWorktrees(view) {
  const ws = state.snap?.focused?.workspace_id;
  const { items } = await call("worktree.list", ws ? { workspace_id: ws } : {});
  if (!items.length) view.append(el("p", "meta", "No worktrees."));
  for (const w of items) {
    const item = el("div", "item");
    item.append(el("div", "title", w.branch || w.id), el("div", "meta", [w.state, w.thread_id].filter(Boolean).join(" · ")));
    if (w.state === "active") {
      const actions = el("div", "row-actions");
      const body = el("div");
      let picks = null;
      // showDiff draws a diff — worktree.diff's or reject_hunks' answer — and keeps its revision,
      // which a reject and a merge both name.
      const showDiff = (d) => {
        picks = hunkPicker(d.files);
        body.replaceChildren(picks.node);
        body.dataset.revision = d.revision;
      };
      actions.append(
        button("Diff", () => call("worktree.diff", { worktree_id: w.id }).then(showDiff).catch(failed)),
        // Rejecting reverts the unchecked hunks in the worktree; what is left is what a merge
        // lands, so "apply only the accepted hunks" is a reject, then a merge (REQ-DSK-002).
        button("Reject unchecked", () => {
          const revision = body.dataset.revision;
          const ids = picks?.unchecked() ?? [];
          if (!revision) { status("Show the diff before rejecting hunks.", true); return; }
          if (!ids.length) { status("Every hunk is checked: nothing to reject.", true); return; }
          call("worktree.reject_hunks", { worktree_id: w.id, revision, hunk_ids: ids }).then(showDiff).catch(failed);
        }, "danger"),
        // A merge lands what the diff showed: it needs the diff read first, passes the
        // revision it was read as, and asks (REQ-WT-003).
        button("Merge", () => {
          const revision = body.dataset.revision;
          if (!revision) { status("Show the diff before merging.", true); return; }
          if (!confirm(`Merge ${w.branch || w.id} as shown in its diff?`)) return;
          call("worktree.merge", { worktree_id: w.id, revision }).then(() => renderView()).catch(failed);
        }, "ok"),
        button("Discard", () => { if (confirm("Discard this worktree and its changes?")) call("worktree.discard", { worktree_id: w.id, confirm: true }).then(() => renderView()).catch(failed); }, "danger"),
      );
      item.append(actions, body);
    }
    view.append(item);
  }
}

// renderFiles is the focused thread's active worktree as a file tree, changed files marked; a
// file opens in a text area, read-only unless the daemon says it is editable, and "Save" sends
// the digest it was read with, so a file changed since is refused (REQ-DSK-003).
async function renderFiles(view) {
  const ws = state.snap?.focused?.workspace_id;
  const { items: worktrees } = await call("worktree.list", ws ? { workspace_id: ws } : {});
  const active = worktrees.filter((w) => w.state === "active");
  if (!active.length) { view.append(el("p", "meta", "No active worktree.")); return; }
  const w = active.find((x) => x.thread_id === state.thread) ?? active[0];
  const tree = await call("worktree.files", { worktree_id: w.id });
  view.append(el("div", "title", w.branch || w.id));
  if (tree.truncated) view.append(el("p", "meta", "Only the first files are listed."));
  const editor = el("div");
  const list = el("div", "files");
  for (const f of tree.items) {
    const row = el("div", "item file" + (f.status ? " changed" : ""), (f.status ? `${f.status[0].toUpperCase()} ` : "  ") + f.path);
    if (f.status !== "deleted") row.addEventListener("click", () => openFile(editor, w.id, f.path).catch(failed));
    list.append(row);
  }
  view.append(list, editor);
}

async function openFile(editor, worktreeID, path) {
  const f = await call("worktree.read_file", { worktree_id: worktreeID, path });
  editor.replaceChildren(el("div", "title", path), el("div", "meta", [f.size_bytes + " bytes", f.editable ? "editable" : "read-only"].join(" · ")));
  if (f.content === null) { editor.append(el("p", "meta", f.binary ? "A binary file is not shown." : "This file is not shown.")); return; }
  const area = document.createElement("textarea");
  area.value = f.content;
  area.readOnly = !f.editable;
  area.spellcheck = false;
  editor.append(area);
  if (f.editable) {
    let sha = f.sha256;
    editor.append(button("Save", () => call("worktree.write_file", { worktree_id: worktreeID, path, content: area.value, expected_sha256: sha })
      .then((r) => { sha = r.sha256; status(`Saved ${path}.`); }).catch(failed), "ok"));
  }
}

async function renderBlocks(view) {
  const pane = state.snap?.panes?.find((p) => p.id === state.focusedPane);
  if (!pane?.session_id) { view.append(el("p", "meta", "No pane.")); return; }
  const { items } = await call("block.list", { session_id: pane.session_id, limit: 50 });
  for (const b of items) {
    const item = el("div", "item");
    item.append(el("div", "title", b.command || "(no command)"), el("div", "meta", [b.state, b.exit_code != null ? "exit " + b.exit_code : null, b.cwd].filter(Boolean).join(" · ")));
    item.addEventListener("click", async () => {
      try {
        const g = await call("block.get", { block_id: b.id, include: "plain" });
        item.querySelector("pre")?.remove();
        item.append(el("pre", "diff", g.output_plain ?? ""));
      } catch (err) { failed(err); }
    });
    view.append(item);
  }
}

// --- events -----------------------------------------------------------------------------

const treeEvents = /^(workspace|tab|pane|layout)\./;
const smokeAnswer = () => String(BigInt(state.smoke) + 1n);
wails.Events.On("frame", (ev) => {
  const { session_id: id, frame } = ev.data;
  const t = state.attached.get(id);
  if (!t) return;
  t.frame = frame;
  draw(t.el, frame);
  if (state.smoke && t.smoked && !t.smokeDone && t.el.textContent.includes("smoke-" + smokeAnswer())) {
    t.smokeDone = true;
    bridge("SmokeDone", t.el.textContent);
  }
});
wails.Events.On("daemon", (ev) => {
  const n = ev.data;
  if (treeEvents.test(n.method)) refresh();
  // A thread's list row changes with its attention and its turn's end; a new thread is
  // announced by its first attention change (API §6).
  const threadChanged = n.method === "thread.attention_changed" || n.method === "thread.turn_finished";
  if (threadChanged && state.view === "threads" && !state.thread) renderView();
  if (n.method === "session.unsubscribed") {
    const t = state.attached.get(n.params.session_id);
    // A remote host's link dropped (REQ-RMT-002): its terminal attaches again when the host is
    // back, which remote.state_changed says.
    if (n.params.reason === "remote_disconnected") status(`remote host ${n.params.session_id.split("/")[0]} disconnected; waiting for it`, true);
    else if (t) bridge("Attach", n.params.session_id, t.cols, t.rows).catch(failed);
  }
  if (n.method === "remote.state_changed") {
    if (n.params.state === "connected") {
      for (const [id, t] of state.attached) {
        if (id.startsWith(n.params.name + "/") && t.cols && t.rows) bridge("Attach", id, t.cols, t.rows).catch(failed);
      }
    } else if (n.params.state === "disconnected") {
      status(`remote host ${n.params.name} disconnected; waiting for it`, true);
    }
    refresh();
  }
  if (n.method === "thread.delta" && n.params.thread_id === state.thread && n.params.kind !== "reasoning") {
    const log = state.transcript.get(state.thread) ?? [];
    const last = log[log.length - 1];
    if (last?.role === "assistant") last.text += n.params.text; else log.push({ role: "assistant", text: n.params.text });
    state.transcript.set(state.thread, log);
    const tr = document.querySelector(".transcript");
    if (tr) { let m = tr.lastElementChild; if (!m || !m.classList.contains("assistant") || m.dataset.live !== "1") { m = el("div", "msg assistant"); m.dataset.live = "1"; tr.append(m); } m.textContent = last?.role === "assistant" ? last.text : n.params.text; }
  }
  if (n.method === "thread.tool_call" && n.params.thread_id === state.thread) {
    // Text after a tool call is a new message: the live text starts again from it.
    const log = state.transcript.get(state.thread) ?? [];
    log.push({ role: "tool", text: `${n.params.tool} ${n.params.status}` });
    state.transcript.set(state.thread, log);
    const tr = document.querySelector(".transcript");
    if (tr) tr.append(el("div", "msg tool", `${n.params.tool} ${n.params.status}`));
  }
  // A notification is shown by the window that receives it: the daemon sends it to the
  // foreground client only, which this window is whenever it sent the last request (API §5.35).
  if (n.method === "notification.posted") notice(n.params);
  // An approval is asked with approval.requested and answered or expired with its thread's
  // tool call changing and its attention leaving `blocked` (API §6).
  if (n.method === "approval.requested" || n.method === "thread.attention_changed" || n.method === "thread.tool_call") {
    if (state.view === "approvals") renderView(); else call("approval.list").then(({ items }) => { $("#approval-count").textContent = items.length ? `(${items.length})` : ""; }).catch(() => {});
  }
  if (n.method === "thread.turn_finished" && n.params.thread_id === state.thread) { state.transcript.delete(state.thread); renderView(); }
});
wails.Events.On("disconnected", (ev) => status("disconnected from umbrald" + (ev.data?.error ? ": " + ev.data.error : ""), true));
// A newer daemon took the sessions over (REQ-HND-001): the bridge dials it and attaches every
// terminal again before saying so, and the page reads its tree and its view again.
wails.Events.On("reconnecting", () => status("the daemon went away; reconnecting", true));
wails.Events.On("reconnected", (ev) => {
  const lost = ev.data?.lost ?? [], attached = new Set(ev.data?.attached ?? []);
  // A terminal opened while the connection was down attached to the closed one: attach it now.
  for (const [id, t] of state.attached) {
    if (!attached.has(id) && !lost.includes(id) && t.cols && t.rows) bridge("Attach", id, t.cols, t.rows).catch(failed);
  }
  status(lost.length ? `reconnected; ${lost.length} terminal(s) are gone` : "reconnected to umbrald", lost.length > 0);
  refresh();
  renderView();
});

// The window is the page and nothing else: no link it draws and no file dropped on it
// navigates the webview away.
document.addEventListener("click", (ev) => { if (ev.target.closest?.("a")) ev.preventDefault(); }, true);
for (const name of ["dragover", "drop"]) document.addEventListener(name, (ev) => ev.preventDefault());

for (const b of document.querySelectorAll("#tabs button")) {
  b.addEventListener("click", () => {
    for (const o of document.querySelectorAll("#tabs button")) o.setAttribute("aria-selected", String(o === b));
    if (b.dataset.view !== "threads") leaveThread();
    state.view = b.dataset.view;
    renderView();
  });
}

(async () => {
  try {
    $("#version").textContent = await bridge("Version");
    state.smoke = await bridge("SmokeMarker");
    state.home = await bridge("Home");
    const s = await call("system.status");
    status(`umbrald ${s.daemon_version ?? ""}`.trim());
    await refresh();
    if (!(state.snap.workspaces ?? []).some((w) => !w.closed_at)) {
      await call("workspace.create", { cwd: state.home });
      await refresh();
    }
    renderView();
  } catch (err) { failed(err); }
})();
