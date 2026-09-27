-- 0002 — the index `block.list` is ordered by (T-F0-10, REQ-BLK-006).
--
-- `blocks` already has three indexes and all of them begin with a column an unfiltered
-- page does not name: `session_id`, `thread_id` or `state`. But the history pane and
-- `umb block list` ask for exactly that, the whole history newest first, so every page was
-- a full scan and a sort of the table: 141 ms at 100,000 blocks against 0.18 ms with this
-- index. `id` is the second column because it is the tie-breaker the cursor pages on, and
-- an index covering only the first column of an ordering still sorts.
--
-- It is a migration of its own rather than an edit to 0001 because 0001 has already been
-- applied to every developer's database, and the runner records only the version it
-- reached (Art. 6, forward-only). Amending a file that will never run again would have
-- left those databases without the index and nothing to notice it: the queries would
-- silently go back to taking 141 ms.

CREATE INDEX idx_blocks_started ON blocks(started_at DESC, id DESC);
