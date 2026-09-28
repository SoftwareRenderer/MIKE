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
- A collapsed parent keeps its row — that row is the control that reopens the branch — and contributes no
  rows below it. Siblings keep their places.
- A `parent_task_id` cycle cuts rather than hangs: each measure carries its own path set.
- A task's effective provider and model are its own `llama_server_url` / `model_name`, else the worker
  default — never the parent row's. So a badge compares two resolutions: `provider · model` when either
  half differs from the parent, nothing when both match, never one half alone. The fork mark is a provider
  difference specifically; the preference that hides badges hides the mark with them.
- A row *is* a `Card`, so drag, the link overlay, multi-select, selection and the action icons are the same
  code as the card's. A capability added to a card is on a row for free; that is the reason rows are not a
  second component.
- The rows one control waits for are tinted from that row's `owners`, on pointer and on keyboard focus
  alike, and focus is real DOM focus (`AGENTS.md → Frontend`). Nothing stores a "highlighted" row.
- Every row's title starts at one x whatever its depth: nothing is indented and the roll-up slot is
  fixed-width, children or not.
- A row's title line holds the title and its badges, and it gives up the action buttons' width only while
  those buttons are drawn — which on touch (`hover: none`) means always, since no `:hover` will ever come
  there. The reserve is the *line's* (`--actions-reserve` as `.card-title`'s padding-right), never the
  title's or a badge's own, so it is paid once however many things share the line.
- A badge never costs a row its title. A title claims space as surplus only (`flex: 1` is a zero basis),
  so an unsquashable badge leaves the line nothing to shrink: the title falls to zero width and the badge
  hangs out of the row. The badges shrink, capped at half the line; the badge's own ellipsis does the
  clipping.
- A line under a row's title — the file count — starts under the title's first glyph, not under the row's
  rule and roll-up slot. One `--title-x`, built on the row from the widths the rule and the slot take, is the
  only source of that x; an indent stated twice drifts. The rule is outside the padding box of both lines, so
  it needs no term in the formula.

## How it is held

`frontend/src/lib/utils/subtask-outline.ts` exports `subtaskOutlineGroups(rootId, childrenMap, expanded,
sortOldestFirst)` and the two shapes it returns — nothing else. One `SubtaskGroup` is one top-level
subtask — `root` plus `rows` — and each `SubtaskRow` carries `task`, `wave`, `owners` (every ancestor below
the card, outermost first) and `progress` (done over total across every descendant). The measure memoises
`wave` and `progress` bottom-up and sorts with `sortByLastMoved`, the comparator the columns use.
`Card.svelte` annotates the rows it renders with `SubtaskRow`, which is why the type is exported.

`Card.svelte` renders the groups as one `.subtask-group` band per sequence of `.subtask-wrapper` rows. The
`outline` prop marks a card as a row and carries the two things only the card holding the outline can know:
how far that row's descendants have got (the roll-up circle), and its parent's resolved model (what the
badge rule compares against). `onRollupHover` names the row whose roll-up circle has the pointer or the
keyboard, and the card holding the outline marks the rows whose `owners` list names it — the tint is decided
where the rows are, not inside the row. Both hosts pass the same props they always did: `Column.svelte` via
`Board.svelte`, and `CardDetail/BoardOverview.svelte`.

The widths that line a row up live in `Card.svelte`'s style block as `--row-slot` and `--row-gap`, and
`.card.is-outline-row` derives `--title-x` from them and paints its own `border-left` from the `--status-color`
the card sets inline. `.rollup-slot` and `.subtask-rollup` read those same vars, so the touch block — where the
slot is 28px, not 18px — widens one declaration and `--title-x` follows.

## Checking this

`cd frontend && npm test` fails it: `frontend/src/lib/utils/__tests__/subtask-outline.unit.ts` holds the
ordering rules and `frontend/src/lib/components/__tests__/card-subtask-outline.unit.svelte.ts` the rendered
shape — row order, one band per branch, the roll-up circle's count, tinting, the badge rule, and which line
of the row holds its badges and its file count. The bar, the painted circle, the reserve appearing with the
buttons (and a touch row's `⋯` being painted rather than merely tappable), the badge's cap and the file
line's x are measured in a browser by `npx playwright test board-card-title-actions`. So is each row's rule:
it carries that row's own status colour while its band-mates keep theirs. That spec measures the
file line's indent with a probe rather than a real task, because a real file count needs a worktree and a
worktree needs an executor turn, which this spec's shared project must not queue (`AGENTS.md → Testing`).
