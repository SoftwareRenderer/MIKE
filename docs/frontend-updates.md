# Frontend Updates — Mechanism

Read this before touching `frontend/src/lib/services/app-update.ts`, the service-worker registration,
`frontend/scripts/build-id.ts` or `GET /api/version`.

A browser keeps the JS it loaded. The generated service worker precaches `index.html` and serves it on
navigation, so a tab that is never closed does not merely keep its old components — every later load of
that install is served the document that was cached with them. This is what makes an install go stale
and what `app-update.ts` exists to prevent.

## What counts as one build

`frontend/scripts/build-id.ts` is a Vite plugin (`apply: 'build'`, `enforce: 'post'`) that runs after
the asset tags are injected. It sorts the `/assets/*.js|css` URLs the finished document loads, hashes
the join, and writes the first 12 hex characters into `<meta name="app-build" content="…">`.

* A digest of the **asset graph**, not of the file: the meta tag is part of the file, so a hash of the
  bytes could never equal a hash that includes them.
* Injected only when that URL list is non-empty, which is why `vite dev` — which serves `/src/main.ts`
  and no hashed assets — ships no stamp at all.
* `GET /api/version` (`backend/server/handler_version.go`) **reads** that tag out of
  `frontendDist/index.html`; it does not recompute the digest. That is the cross-boundary contract: the
  digest's grammar lives in the plugin, and the only thing Go keeps agreeing with is the tag's shape
  (`assetBuildIDPattern`). A second implementation of the digest would be a second claim about what a
  build is, and the two would eventually disagree about a build both can name.
* No dist, an unreadable document, a document over `indexHTMLLimit`, or a tag that is not 12 lowercase
  hex characters → `"assets": ""` with a 200. **A client must read an empty identity as "cannot tell",
  never as "you are up to date"** — that is what distinguishes an old install from a current one when
  the stamp did not exist yet.

The binary's `version` is beside it and is not a substitute: `make frontend` replaces `dist` without
rebuilding the binary. Settings → About shows both for that reason.

## Two authorities, one per client

`compareAndApply()` asks one question in a fixed order, because two answers to "is this stale" is two
chances to reload for nothing:

1. **A registered, active service worker is the authority.** `registration.update()` → the new worker
   installs and stops at `waiting` → `messageSkipWaiting()` → it activates → the `controlling` listener
   with `isUpdate: true` reloads. Nothing is fetched from `/api/version` on this path.
2. **No registered worker: the identity is.** The document's own meta tag against the server's answer;
   different → `location.reload()`, which goes to the network because nothing is intercepting it.

`'serviceWorker' in navigator` being true is not "a worker is registered": the check is
`getRegistration()?.active`.

## Why the worker stays prompt-mode

`vite.config.ts` sets no `autoUpdate` and no `skipWaiting`/`clientsClaim`, so the generated worker is
prompt-mode and a new install sits in `waiting` until someone releases it. The page releases it, from
`compareAndApply()`, at the moment it decided to update — which is when the app is opened.

With `autoUpdate` the worker would take control whenever the browser felt like it, including in the
middle of a sentence. The trade goes the other way too, and is the reason the reload is not deferred
indefinitely: once the new worker activates, `cleanupOutdatedCaches` deletes the previous precache, so
a stale document that lazily imports a chunk it has not fetched yet (CodeMirror, xterm, diff — see
`manualChunks`) can be fetching a URL that no longer exists. A stale document that is kept is not a
safe document.

A `waiting` worker that appears on the browser's own initiative (its check on navigation, its 24-hour
one) is **left waiting**. The open-time check is the only moment anything reloads.

**A new worker only reaches `waiting` when the old one controls a client.** On the first session after
install — the page loaded before any worker existed, and the worker has no `clientsClaim` — the page is
not controlled, so the next worker activates instead of waiting. `update()` then answers `current`, no
reload happens, and the new build lands on the next navigation. That is correct, not a miss: that page
was rendered from the network, and the next one gets the new document. It is also why "no waiting
worker" must never be reported as a fault.

## Once a day, and the loop guard

`checkOnOpen()` runs when the app is opened and returns unless `app-update-last-check-day` in
`$lib/storage` holds a different local day (`year*100 + month…`, an integer that is compared and never
displayed, so no `Intl` and nothing locale-dependent at module scope).

The stamp is written **before** the request, not after. A check that could reload without recording
itself is a reload loop on a server whose answer never changes; and a `saveAppSetting` that throws
(private mode, storage pressure) means the check does not run at all. The same stamp is why the answer
being wrong twice in a day is impossible.

There is deliberately no timer and no timeout anywhere in this path. A long-lived tab is not polled:
the browser's own worker check is left running and the next open collects it.

## Why a reload needs no confirmation

* A task turn runs on the server; the reload does not stop it, and reconnect re-subscribes and replays
  from the per-task buffer (`docs/reconnection.md`).
* The chat draft, the editor drafts and history, the unsaved-file list and the offline queue all live
  in IndexedDB (`$lib/storage`), and `TaskChat`'s `beforeunload` flushes the draft on the way out.
* That handler never calls `preventDefault()`, so a programmatic `location.reload()` raises no browser
  dialog. **If a handler ever starts vetoing unload, this page stops being silently reloadable** —
  that is the assumption to re-check, not a detail.

The exception is the login screen: the master password is the one thing never written anywhere, so a
reload that lands after a manager autofilled it eats the only copy. `isEditingText()` is asked at the
moment of applying — after `registration.update()`, which is the gap the typing happens in — and only
by the automatic caller: it declines, and the update waits for the next open. `checkNow()` never asks,
because a click is intent.

Both halves are tested (`will not reload over text somebody is typing` and `still updates when the
focused field holds nothing`): the guard is a predicate threaded through `compareAndApply` as a
*permission*, and passing the predicate itself rather than its negation inverts it silently — nothing
would ever update, and only the second test says so.

## The one place the answer is spoken

Settings → About (the `about` tab of `ConfigModal.svelte`) has a **Check for updates** button over the
same implementation: `checkNow()` skips the day gate and returns `'applying' | 'current' | 'unknown'`,
rendered inline next to the button. It is inline rather than a `Toast` because the toast is bottom-centre
and behind the modal. It writes the day stamp like any other check, so opening the app afterwards does
not run a second one.

The automatic path has no UI at all: no banner, no badge. A silent update that said nothing *and* did
nothing would be indistinguishable from a dead feature, which is why the manual one answers in all
three cases, including `unknown`.

## Tests

* `src/lib/services/__tests__/app-update.unit.ts` — which authority decides, the day gate (two opens,
  one check), the stamp-before-request rule, every empty-identity guard, the initial-worker
  no-reload rule, and that a failure never reloads.
* `src/lib/components/__tests__/config-modal-check-updates.unit.svelte.ts` — the About row's three
  outcomes, the disabled-while-running rule, and the build chip's presence.
* `backend/server/version_handler_test.go` — the stamp is read out of the built document, and every
  dist that cannot be named answers `""` with a 200. `routes_method_test.go` holds the verb.
* `vite build` is the only thing that proves the plugin stamps the document, and the id it wrote is
  reproducible from the output:
  `grep -o '/assets/[^"]*\.\(js\|css\)' dist/index.html | sort > /tmp/u; printf '%s' "$(cat /tmp/u)" | sha256sum | cut -c1-12`.

The browser half was verified by hand rather than by a committed spec (a spec exists only if it needs a
browser, and this needed one for one afternoon): a fresh context loads and does **not** reload itself,
and a page that the worker controls sees `registration.waiting` appear when `dist/sw.js` changes and
reloads when `SKIP_WAITING` is posted to it. Re-running that by hand means editing `dist/sw.js` (append
a comment) and reloading the page once first so the worker controls the document — without that reload
nothing ever queues behind anything, which reads exactly like the feature being broken.
