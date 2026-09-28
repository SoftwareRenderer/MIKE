# The subtask outline

Owns the order and the shape in which a card lists its descendants: the flat, depth-first row list a board
card and the task view's board overview both draw. It does not own the schedule (that is `backend/exec`),
the collapse state (`expandedTasksStore`), what a column hides (`consolidatedSubtaskIds`), or the card's
progress bar — the bar keeps counting every descendant, collapsed or not.

**A row never precedes its own descendant.** `backend/exec` starts a task only once all its children are
done, so the list puts a descendant above the row that waits for it and a parent reads as the footer of its
own subtree. These two must keep agreeing, case for case.

## What must stay true

- One owner of the ordering: `subtaskOutlineGroups` decides it, `Card.svelte` renders whatever it returns.
- Each descendant is drawn exactly once. A row draws no list of its own — the card holding the outline
  lists the whole subtree — and no row draws a progress bar. `.subtask-progress-bar` stays a direct child
  of `.card`.
- Within a group the rows are ordered by `wave` ascending, ties keeping sibling order, siblings ordered as
  the column orders them. `wave` is 1 for a leaf, otherwise 1 plus the deepest thing under it: the round
  that row becomes runnable. It mirrors `IsStartableNow` and `FindDeepestStartableSubtasks`.
- A group is contiguous and holds exactly one top-level subtask's branch: that subtask and everything that
  must finish before it. The band carries only a wash of its root's colour; each row's own status is its
  `border-left`, so a row still in backlog reads grey beneath the done row it waits for. One rule per band
  would paint every row with the branch head's status.
- A collapsed parent keeps its row and contributes no rows below it; its own card collapses it, from the
  progress bar under that card's title. Siblings keep their places.
- A `parent_task_id` cycle cuts rather than hangs: each measure carries its own path set.
- A task's effective provider and model are its own `llama_server_url` / `model_name`, else the worker
  default — never the parent row's. So a badge compares two resolutions: `provider · model` when either
  half differs from the parent, nothing when both match, never one half alone. The fork mark is a provider
  difference specifically; the preference that hides badges hides the mark with them.
- A row *is* a `Card`, so drag, the link overlay, multi-select, selection and the action icons are the same
  code as the card's. A capability added to a card is on a row for free; that is the reason rows are not a
  second component.
- **A row has no leading control and no indent.** Its title line, and its second line when it has one, both
  start at the row's padding box. So the first glyph of every row sits at one x — whatever its depth, its
  children or its status.
- A row draws a second line only when it has something to report — the file count, or a model of its own.
  A row with neither is one line tall, and no height is reserved for a control that is not there.
- A row's title line holds the title, the status word and the memory alert; its second line holds the file
  count and the model badge. The title gives up the action buttons' width only while those buttons are
  drawn — which on touch (`hover: none`) means always, since no `:hover` will ever come there. The reserve
  is the *line's* (`--actions-reserve` as `.card-title`'s padding-right), never the title's or a badge's
  own, so it is paid once however many things share the line.
- A badge never costs a row its title. A title claims space as surplus only (`flex: 1` is a zero basis),
  so an unsquashable badge leaves the line nothing to shrink: the title falls to zero width and the badge
  hangs out of the row. The badges shrink, capped at half the line; the badge's own ellipsis does the
  clipping. On the second line the file count keeps its width and `.meta-badges` is what gives way.

## How it is held

`frontend/src/lib/utils/subtask-outline.ts` exports `subtaskOutlineGroups(rootId, childrenMap, expanded,
sortOldestFirst)` and the two shapes it returns — nothing else. One `SubtaskGroup` is one top-level
subtask — `root` plus `rows` — and each `SubtaskRow` carries `task`, `wave`, and `owners` (every ancestor
below the card, outermost first, so its last entry is the row's own parent). The measure memoises `wave`
bottom-up and sorts with `sortByLastMoved`, the comparator the columns use. `Card.svelte` annotates the rows
it renders with `SubtaskRow`, which is why the type is exported.

`Card.svelte` renders the groups as one `.subtask-group` band per sequence of `.subtask-wrapper` rows. The
`outline` prop marks a card as a row and carries the one thing only the card holding the outline can know:
the row's parent's resolved model, which is what the badge rule compares against. Both hosts pass it the
same way: `Column.svelte` via `Board.svelte`, and `CardDetail/BoardOverview.svelte`.

A row owns no width of its own beyond its `padding: 0 12px`, the card's own padding. The title line and the second line are ordinary
children of that padding box, so neither carries an indent, and the action buttons anchor to the title line
as they do on a card. `.card.is-outline-row` paints its `border-left` from the `--status-color` the card
sets inline. A row is the 30px title line, plus the shared `.diff-stats-row` when it reports something; a
`.badge` in that line is what makes it taller than the file count's line box, and `.meta-badges` keeps the
pair to one line.

## Checking this

`cd frontend && npm test` fails it. `frontend/src/lib/utils/__tests__/subtask-outline.unit.ts` holds the
ordering rules; `frontend/src/lib/components/__tests__/card-subtask-outline.unit.svelte.ts` the rendered
shape: row order, one band per branch, a collapsed branch contributing no rows below its own, the badge
rule, and which line holds the status word, the file count and the model badge. The bar, the reserve
appearing with the buttons (and a touch row's `⋯` being painted rather than merely tappable) and the badge's
cap are measured in a browser by `npx playwright test board-card-title-actions`. So is each row's rule: it
carries that row's own status colour while its band-mates keep theirs.
