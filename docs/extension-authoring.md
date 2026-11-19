# Authoring a custom extension

Read this before touching `backend/extensions/dev.go`, `dev_load.go`, `scaffold.go`, the
`/extensions/dev*` routes, or `templates/extension/`. The rules are in `AGENTS.md → Extensions`; this
file is the mechanism.

A person gives a **name** and one sentence about what the extension should do. They never choose a
type: the agent in the first task reads the authoring skill, decides the type, and writes the
manifest. The whole loop is create → the agent writes → **Try Extension** → refresh the tab.

## The namespace

`extensions/_dev/<slug>` is the repository, and `<slug>` comes from `DevSlug` — the one slugifier,
whose output is letters, digits and inner dashes. That is the reservation's whole safety: no name can
be slugged into `_dev` or `_try`, so the directories the server owns can never be a person's
extension name.

| Path | Who owns it |
|---|---|
| `extensions/_dev/<slug>/` | the person's repository; a git repo the tasks commit to |
| `extensions/_dev/_try/<slug>/<taskID>/` | the server's loaded copy of one task's uncommitted bytes |

Nothing in the namespace is installable: `scanLocalExtensions` and `findLocalExtension` skip it, and
`install-local` refuses `_dev` as an author. Deleting the **row** deletes the staging copy only; the
repository goes with the **project**, through `RemoveDevRepo`, which removes nothing that is not
strictly beneath the namespace and refuses a path that resolves out of it.

`/extensions/` refuses every dot-prefixed segment (`noEscapeFS`), which is what lets a git repository
live under the served root: `.git/config`, `.env` and `.agents/skills/**` are unreachable.

## The row

A development row is `install_source = "dev"`, `project_id` set, no manifest, **not enabled**. It is
inert until something is loaded: `IsLoaded()` is false, every enabled-row reader skips it, and the
enable path refuses it. `devOrExtensionDir` is the single answer to "where do this row's files live"
— the served URL, the prompt overlay and the LLM mappings all resolve through it, so the browser's
version and the server's version are never two different things.

## Loading

One entry point, `ExtensionHandler.LoadDev(id, taskID)`, behind `POST /api/extensions/{id}/reload`:

- **no task id** loads the repository, served in place. This is what the Extensions card's Reload will
  call once that card grows one.
- **a task id** (the ReviewBar's Try Extension) loads that task's worktree. A worktree lives outside
  the served root, so its files are copied into `_dev/_try/<slug>/<taskID>` first; the copy is
  replaced on every load, so a file the source dropped stops being served.

`GET /api/extensions/dev` lists the rows beside the repositories that no row claims, and
`POST /api/extensions/dev-attach` makes a row for one of them — the way back after an uninstall, which
takes the row away and leaves the repository. Neither has a caller in the browser yet: the ordinary
listing already carries a row's loaded state and served URL, and only the unclaimed-repository half
needs this pair.

Every directory the load reads is derived from stored rows — the project's path, the task's worktree —
never from a path in a request. A task must belong to the row's project. A task whose worktree is gone
is **refused**, never answered with the repository's bytes: showing merged code while the person
believes they are looking at the task's is worse than an error.

The copy takes the directory's own files (a manifest names its stylesheet, never the script its own
`index.html` loads) plus every name a capability resolves, and applies the caps before the bytes are
read. A link among the directory's own files is skipped — a worktree carries them; a link where the
manifest says a file is is refused, because the served copy would silently hold the wrong bytes.

`validateLoadedManifest` is the list an install runs, shared by both paths, so "it installed" and "it
loaded" cannot mean different things. A manifest that fails validation leaves the version already running
in place and answers with the failure text — the sentence the person relays to the agent.

### The access question

Validation checks a manifest's shape; it does not ask anyone whether the shape is acceptable. The agent
wrote this manifest, so the load asks the question an install asks: `consentRequired`
(`backend/extensions/consent.go`) refuses a version declaring the `network` capability, a sandbox-escaping
permission (`network.proxy`, `preview.serveScripts`, `preview.serveNetwork`), or a folder on this machine
— the three conditions `requiresInstallReview()` reads before the install dialog opens. The rule lives
once per end and nowhere else: `consent_test.go` and
`frontend/src/lib/utils/__tests__/extension-consent-gate.unit.ts` run the same fixtures through each half,
so a condition added on one side only fails a test.

`POST /api/extensions/{id}/reload` answers **409** with what the dialog renders — the entry and the folder
proposals read off the manifest about to run, not off the browser's cached row — plus a fingerprint of the
consent-bearing fields. The browser opens `ExtensionInstallConfirmModal` with them and presses again
carrying that fingerprint, which the server accepts only when it equals the one it computed; the client's
word alone decides nothing. `TryExtension` (`frontend/src/lib/composables/use-try-extension.svelte.ts`)
owns the three states that press needs, which is why the flow has a test without mounting the chat.

The check runs **before** the worktree is copied, so a refused version is never served, and it writes no
row: the version already running keeps running, and closing the dialog posts nothing. What the row keeps
(`network_ack`) is a fingerprint, not a flag. Re-loading unchanged bytes asks nothing; one more host in
`allowed_domains` asks again. A version that asks for nothing carries no acknowledgement, which is what
stops an old yes carrying a later ask. Granting the folders in the dialog applies them through
`PUT /api/extensions/{id}/agent-mount`, the same route the card's mount control uses, so the folders a
person named are still judged by `ValidateAgentMountGrant`.

Test: `go test ./backend/extensions/ -run Consent` over the conditions, the fingerprint and the refusal;
`backend/extensions/dev_load_test.go` over the load itself, and
`frontend/src/lib/composables/__tests__/try-extension.unit.svelte.ts` over the dialog, the echo and the
decline.

## Creating

`POST /api/extensions/scaffold` writes the repository from the embedded bundle
(`templates/extension/`, injected into `NewServer` the way the frontend assets are), commits it,
creates the project, the row and the first task, and starts that task through the ordinary schedule.
Any failure removes what the call made, including the directory: a project whose repository is
half-written is a state nobody can clean up. The task's worktree is the ordinary one under
`.worktrees/`, never inside the extensions tree.

That first card has no browser in front of it choosing a model, so `applyTaskDefaults`
(`backend/api/task_defaults.go`) gives it the configured worker pair, operating mode, failed-turn limit
and auto-commit defaults. A row with no provider is one `executor.StartTask` refuses, and the create only
logs that refusal — the pair is what makes the card run.

## The bundle

`templates/extension/` holds `README.md`, `AGENTS.md`, a `tests/README.md`, and the authoring skill
under `.agents/skills/extension-authoring/` — the project's own skill root, so the container binds it
read-only while the browser cannot reach it.

## Tests

`go test ./backend/extensions/ ./backend/api/` is the gate over this whole subsystem:

- `dev_test.go` — the namespace, delete, listing, enable and repair guards.
- `dev_load_test.go` — loading, the three consumers resolving into the staging copy, the prune.
- `scaffold_test.go` — the bundle and the scaffolder: every example manifest passes the load validator,
  and the grammar page names every field the Go manifest types declare, in both directions.
- `backend/api/extension_scaffold_test.go` — the create path, and the defaults its first task arrives
  with.
- `backend/api/project_dev_repo_test.go` — only a development project's directory is removed.
