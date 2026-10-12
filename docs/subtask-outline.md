# The subtask outline

Defines how a card lists its descendants: a flat, depth-first list of rows used by both the board card and the task view's board overview. It does not handle the schedule (`backend/exec`), collapse state (`expandedTasksStore`), column hiding (`consolidatedSubtaskIds`), or the progress bar (which counts all descendants regardless of collapse state).

**A row never precedes its own descendant.** `backend/exec` only starts a task after its children are finished; thus, descendants appear above the row that waits for them, and a parent appears as the footer of its subtree. The list and the executor must always agree.

## What must stay true

- **Ordering owner**: `subtaskOutlineGroups` determines the order, and `Card.svelte` renders it.
- **Single entry**: Each descendant is drawn exactly once.
- **No nested lists**: Rows do not draw their own lists or progress bars; the card holding the outline lists the entire subtree. The `.subtask-progress-bar` is a direct child of `.card`.
- **Wave ordering**: Rows in a group are ordered by `wave` ascending. Ties follow sibling order, which matches the column's ordering. `wave` is 1 for leaf tasks, or 1 plus the deepest descendant's wave (the round the row becomes runnable). This mirrors `IsStartableNow` and `FindDeepestStartableSubtasks`.
- **Group structure**: A group is a contiguous block containing one top-level subtask and all its required descendants. The group band uses the root's color, but each row's `border-left` shows its own status. For example, a backlog row appears grey beneath a completed row it waits for. Applying a single color to the band would incorrectly show the root's status for all rows.
- **Collapse behavior**: A collapsed parent retains its row but hides its descendants. The parent card is collapsed via the progress bar under its title. Siblings keep their places.
- **Cycle protection**: Cycles in `parent_task_id` are cut to prevent hangs; each measure uses its own path set.
- **Model resolution**: A task uses its own `llama_server_url` and `model_name`, or the worker default, never the parent's. Badges show `provider · model` if either differs from the parent; otherwise, they are hidden. They never show only one half. The fork mark specifically indicates a provider difference; it is hidden if badges are disabled.
- **Component reuse**: A row is a `Card` component, sharing its drag, link overlay, selection, and action icon logic. Adding a capability to `Card` automatically enables it for rows.
- **Alignment**: **A row has no leading control and no indent.** The title line and any second line start at the row's padding box. The first character of every row aligns vertically, regardless of depth or status.
- **Dynamic height**: A second line is only drawn if the row reports a file count or its own model. Otherwise, the row is one line tall.
- **Line layout**: The title line contains the title, status, and memory alert; the second line holds the file count and model badge. The title reserves space for action buttons only when they are visible (always on touch devices). The reserve (`--actions-reserve`) is applied to the line's padding-right, not to individual titles or badges.
- **Badge constraints**: Titles use `flex: 1` with zero basis; if a badge is too wide, the title may shrink to zero and the badge may overflow. Badges shrink to a maximum of half the line width, using ellipsis for clipping. On the second line, the file count retains its width, and `.meta-badges` shrinks first.

## How it is held

`frontend/src/lib/utils/subtask-outline.ts` only exports `subtaskOutlineGroups` and its return types. A `SubtaskGroup` represents a top-level subtask (root and rows). Each `SubtaskRow` includes the task, `wave`, and `owners` (ancestors below the card, from outermost to parent). The function memoizes `wave` bottom-up and uses `sortByLastMoved` for sorting, matching the columns.

`Card.svelte` renders the groups as one `.subtask-group` band per sequence of `.subtask-wrapper` rows. The `outline` prop marks a card as a row and provides the parent's resolved model for badge comparison. Both hosts pass it the same way: `Column.svelte` via `Board.svelte`, and `CardDetail/BoardOverview.svelte`.

A row owns no width of its own beyond its `padding: 0 12px` (the card's padding). The title line and second line are ordinary children of that padding box, so they carry no indent. Action buttons anchor to the title line. `.card.is-outline-row` paints its `border-left` using the `--status-color` set inline. A row is 30px (title line) plus the shared `.diff-stats-row` when reporting data. A `.badge` in that line makes it taller than the file count's line box, and `.meta-badges` keeps the pair to one line.

## Checking this

`cd frontend && npm test` fails it. `frontend/src/lib/utils/__tests__/subtask-outline.unit.ts` holds the ordering rules; `frontend/src/lib/components/__tests__/card-subtask-outline.unit.svelte.ts` the rendered shape: row order, one band per branch, collapsed branch behavior, the badge rule, and line contents. The progress bar, the action button reserve (including touch rows), and the badge cap are measured via `npx playwright test board-card-title-actions`. This also verifies that each row carries its own status color within its band.
