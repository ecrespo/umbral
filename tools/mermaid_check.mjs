// Validates every ```mermaid block in the repository's Markdown files with mermaid.parse().
// Usage: npm install --prefix tools && node tools/mermaid_check.mjs [files...]
import { JSDOM } from "jsdom";
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join, relative } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(fileURLToPath(new URL(".", import.meta.url)), "..");
const dom = new JSDOM("<!DOCTYPE html><body></body>");
Object.assign(globalThis, {
  window: dom.window, document: dom.window.document, DOMParser: dom.window.DOMParser,
  Element: dom.window.Element, HTMLElement: dom.window.HTMLElement, Node: dom.window.Node,
});
try { Object.defineProperty(globalThis, "navigator", { value: dom.window.navigator, configurable: true }); } catch {}
const { default: mermaid } = await import("mermaid");
mermaid.initialize({ startOnLoad: false });

const SKIP = new Set(["node_modules", ".git", "dist"]);
function walk(dir, out = []) {
  for (const name of readdirSync(dir)) {
    if (SKIP.has(name)) continue;
    const p = join(dir, name);
    if (statSync(p).isDirectory()) walk(p, out);
    else if (name.endsWith(".md")) out.push(p);
  }
  return out;
}

const files = process.argv.length > 2 ? process.argv.slice(2) : walk(root);
let total = 0, failed = 0;
for (const f of files) {
  const blocks = [...readFileSync(f, "utf8").matchAll(/```mermaid\n([\s\S]*?)```/g)].map((m) => m[1]);
  for (const [i, b] of blocks.entries()) {
    total++;
    try { await mermaid.parse(b); }
    catch (e) { failed++; console.error(`FAIL ${relative(root, f)} #${i + 1}: ${String(e.message).split("\n")[0]}`); }
  }
}
console.log(`mermaid: ${total - failed}/${total} diagrams valid`);
process.exit(failed ? 1 : 0);
