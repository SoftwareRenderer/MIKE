# Review notifications

One preference, `notifyOnReview`, adds a browser notification for a task that enters Review. Both halves
of the feature are the client's: where the choice is kept, and the rule that decides whether an arrival
is worth telling someone about. The server stores nothing about notifications and is never told about one.

## Where the flag lives

Permission and delivery belong to a browser, so the choice does too: saved on the server it would apply
one browser's answer to every other browser this person opens. `uiStore.notifyOnReview` (default off) is
the app's one read of it, loaded from and written back to the `notify-on-review` key of the `app-settings`
store through `$lib/storage`.

Settings still owns the editing: `ConfigModal.svelte` hydrates its row from the store when the dialog
opens and writes the store inside `save()`, so Cancel discards an unsaved toggle like every other
preference on that form. Sign-out erases it with the rest of the stores (`clearAll()`). A
`notify_on_review` row an older server left in the `config` table is read by nothing; the flag is off in
each browser until it is switched on there.

## Why the client detects the arrival

The natural design — the executor broadcasts "this task landed in Review" — has no place to live. About
25 sites in `backend/exec` set `Status = types.StatusReview` (turn end, failure, loop detection, sandbox
refusal, consolidation), and the transition would have to be recognised at each of them.

Two frames the server already sends carry the same news:

| frame | reaches | says |
|---|---|---|
| `project_counts` | every client, on every task row change | each project's Review **count** |
| `task_updated` / `board_refresh` | clients subscribed to that project | the board of one project |

So a rise in a project's Review count is the signal, and the project's board says which cards are in
that column. Nothing is subscribed to a project the user is not looking at, which is why the notifier
fetches that project's board (`GET /projects/{id}/board`, the same cached endpoint the offline path
uses) rather than waiting for a frame that will never arrive. A client that is only ever on one board
therefore still reports arrivals in every project.

## The ledger

`$lib/services/review-notifications.svelte.ts` keeps one ledger and lets either frame drive it,
because either can arrive first:

- **counts** per project — from the last `project_counts` frame. A project missing from a frame is
  dropped; its column is gone from this client's world.
- **columns** per project — `{ count, ids }`, the Review column as last read (from the board on screen
  or from a fetch). A read that describes the *pre-rise* count is the baseline that makes the arrival
  diff exact; when it does not match, the count alone is the baseline.
- **announced** per project — `taskId → moved_at`. `store.updateTask` stamps `moved_at` on a column
  change and on nothing else, so recording the move rather than the task is what makes the same arrival
  announce once while a task that left Review and came back is news again.

The first sight of a project only seeds: neither a page load nor a trip back to a project may spend a
notification on changes that were already there. While the preference is off the counts are still
counted but nothing is fetched and nothing announced, so switching it on does not deliver the backlog
of rises that happened while it was off.

Which Review cards are new, and whether to say so, is pure logic in `$lib/utils/review-notify.ts`
(`reviewArrivals`, `columnInViewport`, `shouldNotifyReview`, `notableArrivals`) — the service holds no
rules of its own.

## When it stays quiet

A notification is due when the preference is on **and** the user is not looking at the Review column
**and** the task is not the one they have open.

- **Looking at the Review column** is `uiStore.reviewColumnInViewOf`, written only by `Board.svelte`:
  a majority of the Review column's width inside the `.board` scroll viewport, measured on mount, on
  board scroll, on window resize, and whenever a card changes column. It names *which* project's board
  is showing that column, so an arrival elsewhere is still news. On a wide board all four columns sit
  inside the viewport, so a desktop user watching that project's board is "looking at Review". The
  board unmounts behind a task detail or an explorer, and its effect's teardown clears the flag — an
  unmounted board is nobody's line of sight.
- **The document** must be visible and focused (`document.visibilityState`, `document.hasFocus()`); a
  background or unfocused tab is never looking at anything.
- **The open task** is `appStore.selectedTask` or the floating chat's task.
- **A move this tab made itself** is not news: `moveTask` in `$lib/api/tasks.ts` is the only way the
  client changes a status, so it marks the task as self-moved for 60 s, covering drag, card menu,
  multi-select move, board DnD and the task view alike.

Several arrivals in one read are one notification, naming the count.

## Taking one back

A notification the user has now seen is closed rather than left to time out. The same two facts decide
it, read as state instead of as a gate: the open task retires its own `mike-task-<id>`, and
`reviewColumnInViewOf` retires every notification of that project — the batch `mike-review-<project>`
included. Opening one of the tasks a batch speaks for retires nothing of the batch, because it also
stands for the tasks still unread in it. `dismissedTags` holds the rule; one `$effect` in `App.svelte`
reads `selectedTask`, `floatingTask` and `reviewColumnInViewOf` and hands the result to the service,
which decides nothing of its own.

- **Visible and focused, read at the moment the state changes rather than watched** — focus is not a
  reactive value, so a tab that merely regains focus keeps what it already had.
- **Dismissal is by tag.** `dismissBrowserNotification` closes the objects this page holds and asks the
  live registration for `getNotifications({ tag })`, so a service-worker notification that outlived a
  reload goes too. A constructor-shown one does not: `getNotifications` answers only for what
  `showNotification` showed, and a reload drops the reference to the rest. Hence the hold-by-tag map in
  `notifications.ts` — it is what makes a notification dismissible while its page lives.
- **The record is not a leak.** A tag leaves the ledger when it is retired, and when its project
  disappears from a `project_counts` frame — the same rule the ledger applies to its own per-project
  state. A task's record is deliberately *not* dropped when the task leaves Review: the open-task rule
  still applies to a card that has since moved on.

## Permission and delivery

`$lib/utils/notifications.ts` is the only place that touches the Notification API, and every entry
point is safe where it does not exist (plain-http origin, webview, Safari's page-context ban).

- Permission is asked for on the click that enables the preference — the only user gesture available —
  and the Preferences row then states what the browser answered: blocked, or no notifications at all.
  Nothing is disabled or refused; the flag still persists.
- Delivery is `new Notification` with a click handler that focuses the tab and opens the task. Where
  the constructor throws, it falls back to `registration.showNotification`; the worker is the
  generated one (`vite-plugin-pwa`, workbox `generateSW`), so that path carries no click handler and
  cannot open the task.
- `tag` is `mike-task-<id>` (or `mike-review-<project>` for a batch), so a task that keeps moving
  replaces its own notification instead of stacking, and so the tag alone is enough to take one back
  later — see *Taking one back*.

## Tests

- `utils/__tests__/review-notify.unit.ts` — the arrival diff, the viewport geometry, the gate, the
  retirement rule.
- `utils/__tests__/notifications.unit.ts` — permission answers, both delivery paths, and closing a tag
  through the held objects and through the registration.
- `services/__tests__/review-notifications.unit.svelte.ts` — the ledger: seeding, one announcement per
  arrival, the suppression cases, the preference off, and taking a notification back.
- `components/__tests__/preferences-tab.unit.svelte.ts` — the row and its permission sentences.
- `stores/__tests__/notify-on-review-persist.unit.ts` — the flag is loaded from this browser's storage,
  anything but a stored `true` leaves it off, and the save writes the key back.

No Playwright spec: a real notification permission cannot be driven headless, and everything that
decides is pure or unit-tested above.
