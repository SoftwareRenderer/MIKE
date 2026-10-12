# Review Notifications

The `notifyOnReview` preference enables browser notifications when a task enters Review. The entire feature is client-side: the server neither stores nor tracks notifications.

## Preference & Storage

Permission and delivery are browser-specific, so the preference is stored locally: `uiStore.notifyOnReview` is loaded from the `notify-on-review` key in the `app-settings` store via `$lib/storage`.

`ConfigModal.svelte` handles editing; changes are saved to the store on `save()` and discarded on Cancel. Sign-out clears this preference via `clearAll()`.

## Arrival Detection

Since the server does not broadcast specific "Review arrival" events, the client detects them by monitoring:
1. **`project_counts` frame**: Tracks the Review count for each project.
2. **`task_updated` / `board_refresh` frames**: Updates the project's board.

When a project's Review count rises, the client fetches the project's board (`GET /projects/{id}/board`) to identify which tasks moved into Review. This ensures arrivals are reported even for projects not currently on screen.

### The Ledger
`$lib/services/review-notifications.svelte.ts` maintains:
- **Counts**: Latest Review counts per project.
- **Columns**: The Review column's state (`{ count, ids }`) to detect exact diffs.
- **Announced**: A map of `taskId → moved_at` to ensure each arrival is announced only once (unless the task leaves and returns).

Pure logic for determining notable arrivals and notification plans lives in `$lib/utils/review-notify.ts`.

## Suppression Rules

A notification is suppressed if:
- The preference is off.
- The user is looking at the Review column (a majority of the column is in the viewport).
- The task is currently open (selected task or floating chat).
- The tab is hidden or unfocused (`document.visibilityState`, `document.hasFocus()`).
- The move was made by the current tab (`moveTask` marks the task as self-moved for 60s).

**Batching**: Multiple arrivals are grouped into a single notification. The notice is flushed when all related board fetches settle. If multiple projects have arrivals, the notification points to the project with the most tasks in Review.

## Dismissal

Notifications are closed when:
- The corresponding task is opened.
- The project's Review column comes into view.

Dismissal is handled by tag (`mike-task-<id>` or `mike-review-batch`). `$lib/utils/notifications.ts` uses `getNotifications({ tag })` to close both page-shown and service-worker notifications.

## Permission & Delivery

`$lib/utils/notifications.ts` manages the Notification API:
- **Permission**: Requested when the preference is enabled.
- **Delivery**: Uses `new Notification`. If the constructor throws, it falls back to the service worker.
- **Service Worker**: `frontend/public/sw-notifications.js` handles clicks. It either focuses an existing window and posts the target or opens a new window via the target URL.
- **Public Access**: `/sw-notifications.js` is public (`isPublicPath`) because the worker fetches it without a session.

## Verification

- **Unit Tests**: `review-notify.unit.ts`, `notifications.unit.ts`, `review-notifications.unit.svelte.ts`, `preferences-tab.unit.svelte.ts`, `notify-on-review-persist.unit.ts`.
- **Backend Test**: `TestWorkerScriptNeedsNoSession` verifies the worker script is accessible without a session.
- **Command**: `cd frontend && npm test` covers the arrival rules and ledger.

Note: No Playwright specs exist because notification permissions cannot be driven headless.
