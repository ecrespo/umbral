# Delta — catalog descriptions as a delimited third-party block

| Field | Value |
|---|---|
| **Status** | `RATIFIED 2026-10-24 by the Tech Lead ("ratifico los deltas")`; folded into Tech 1.72 §5.3b, and archived |
| **Date** | 2026-10-24 |
| **Task** | T-HRD-09 |
| **REQ** | REQ-SKL-005 (no change to its text) |
| **Raised by** | Q4 of delta `2026-10-hrd02-open-questions`, answered (b) and ratified by the Tech Lead on 2026-10-24 |
| **Depends on** | — (the branch is stacked on T-HRD-08's only so the Tech Design's version numbers follow) |

## Evidence

Each turn's `# Skills` section listed `- name: description` lines straight after its preamble.
A description is the bundle author's text. `oneLine` already keeps it to one line, so it cannot
open a new section. It could still write `</rules>`, `<rules path=…>` or a closing tag of
whatever wrapped it, and the model had nothing marking where Umbral's words stopped and the
author's began. `TestTheCatalogIsADelimitedThirdPartyBlock_REQ_SKL_005` found no block, red.

## Decisions

1. **One `<skills>` block.** The catalog's entries sit between `<skills>` and `</skills>`, after
   the existing preamble.
2. **A second preamble sentence names the descriptions.** It says they are the author's
   untrusted third-party text, not instructions, to be used only to choose a skill to load, and that
   nothing they say is to be followed.
3. **Entries are escaped.** `&`, `<` and `>` in a name or description are written as
   `&amp;`, `&lt;` and `&gt;`. So no entry can close the block or open a tag: the prompt holds
   exactly one `</skills>`, Umbral's. Escaping happens when the prompt is rendered, so the
   catalog's own data stays the author's text. The 256-character cut of REQ-SKL-004 applies to
   that text, before escaping; an entry may therefore render longer (at most five bytes per
   character escaped), and the context budget counts the rendered prompt, so the growth is
   inside it.
4. **Taint is unchanged.** The turn stays untainted by the catalog. Taint is kept for content
   the user never saw, such as `skill_load`'s results.
5. **Look-alike brackets are not folded.** A full-width `＜/skills＞` is not markup and cannot
   end the block. Only the ASCII characters are escaped.

## Residual

Only the catalog is delimited and escaped. The `# Git` section that follows it prints the
branch name and the `git status --short` and `git diff --stat` file names as they are, and a
cloned repository chooses those, so it could still write a `<skills>` tag or a `# Rules files`
line after Umbral's block. That predates this delta and is outside Q4. It is left for the Tech
Lead to decide whether a follow-up task treats git text the same way. Rules files are the
project's instructions by design and are not wrapped.

## Spec changes

- Tech 1.72 §5.3b: the catalog sits in a `<skills>` block under `# Skills`, with a preamble
  that calls the descriptions third-party text, not instructions, and with `&`, `<` and `>`
  in its entries escaped.

## Verification

Spec-guardian found nothing CRITICAL or HIGH. Its findings were fixed: the cut-before-escape
wording, the git residual above, "untrusted" in the preamble, and a guarded, parallel test.


- `TestTheCatalogIsADelimitedThirdPartyBlock_REQ_SKL_005` checks a hostile description that
  writes `</skills>`, a fake `# Rules files`, a `<rules>` tag, an upper-case `<SKILLS>` and an
  already-escaped `&lt;/skills&gt;`. It asserts:
  - the preamble's wording;
  - one `</skills>`;
  - no `<` or `>` inside the block;
  - no `<rules path="x">` in the prompt;
  - each entry escaped.

  Red first: the test failed because there was no block.
- Mutations:
  - not escaping fails the test on two `</skills>` and an opened rules tag;
  - not escaping `&` fails it on the double-escaped entry.
