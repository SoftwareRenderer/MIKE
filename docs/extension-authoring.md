# Authoring a custom extension

Read this before touching `backend/extensions/dev.go`, `dev_load.go`, `scaffold.go`, the
`/api/extensions/{id}/reload` route, or `templates/extension/`. The rules are in `AGENTS.md → Extensions`;
this file is the mechanism.

A person gives a **name** and one sentence about what the extension should do. They never choose a
type: the agent in the first task reads the authoring skill, decides the type, and writes the
manifest. The whole loop is create → the agent writes → **Try Extension**, which loads the version and
reloads the tab.

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
`install-local` refuses `_dev` as an author. Nothing that deletes a row, a task or a project removes the
repository: `RemoveDevRepo` unwinds the directory a create made and nothing else, and it removes nothing
that is not strictly beneath the namespace and refuses a path that resolves out of it. A repository whose
project is gone is an **unclaimed** directory — the person's own folder to read, keep, rename or remove.
That is what keeps the project-removal dialog's promise ("The project folder itself will NOT be deleted",
`app.project.removeMessage`) true of an authoring project as much as of any other: the server created that
folder, and still never deletes it.

`/extensions/` refuses every dot-prefixed segment (`noEscapeFS`), which is what lets a git repository
live under the served root: `.git/config`, `.env` and `.agents/skills/**` are unreachable.

## The row

A development row is `install_source = "dev"`, `project_id` set, no manifest, **not enabled**. It is
inert until something is loaded: `IsLoaded()` is false, every enabled-row reader skips it, and the
enable path refuses it. `devOrExtensionDir` is the single answer to "where do this row's files live"
— the served URL, the prompt overlay and the LLM mappings all resolve through it, so the browser's
version and the server's version are never two different things.

### The name

Every development row is `(author = "operator", name = <slug>)`, and `(author, name)` is `UNIQUE`, so one
row holds one name. A taken name is the person's own collision and is answered as one: the index refuses the
insert and `isDuplicateName` turns that into a conflict naming the extension, so SQLite's text never walks
out of a handler as an internal error.

`POST /api/extensions/scaffold` asks the name before it writes a byte, through `ReserveDevName`. A row keeps
the name while anything still owns it: a repository on disk, a project that exists, a project holding that
path, or a value the person left in the row. The create is then refused with 409, having made nothing — what
to do with that folder is the person's decision. A row with none of those is the remainder of a create that
died after writing it: it is deleted through `deleteExtension` (docs/extension-lifecycle.md → Deleting an
extension deletes its directory) and an Info line names it. A deletion may take that row because it holds
nothing of anyone's — `devRowIsLeftover` is the one place that decides, and it reads the row's loaded state,
its config, its acknowledgement and its stored keys.

A project's deletion takes its own development rows with it, through `DeleteDevRows`, and removes no file:
a row beside a deleted project claims a name, can never load, and has no other owner. The scaffold path
rolls its own row back the same way when a later step of the create fails.

Tests: `backend/extensions/dev_create_test.go`, `backend/api/extension_scaffold_test.go`,
`backend/api/project_dev_repo_test.go`.

## Loading

One entry point, `ExtensionHandler.LoadDev(id, taskID)`, behind `POST /api/extensions/{id}/reload`:

- **no task id** loads the repository, served in place. This is what the Extensions card's Reload will
  call once that card grows one.
- **a task id** (the ReviewBar's Try Extension, offered only on a task in Review) loads that task's
  worktree. A worktree lives outside the served root, so its files are copied into
  `_dev/_try/<slug>/<taskID>` first; the copy is replaced on every load, so a file the source dropped
  stops being served.

Uninstalling a row leaves its repository: the row's deletion takes the staging copy and nothing else
(docs/extension-lifecycle.md → Deleting an extension deletes its directory). An unclaimed folder is the
person's own from then on — read, keep, rename or remove — and a create of the same name is refused while
the folder stands, with the folder named in the sentence. No route lists unclaimed repositories or gives one
a row: the ordinary listing carries a row's loaded state and served URL, and nothing in the browser ever
called the pair that did.

Every directory the load reads is derived from stored rows — the project's path, the task's worktree —
never from a path in a request. A task must belong to the row's project. A task whose worktree is gone
is **refused**, never answered with the repository's bytes: showing merged code while the person
believes they are looking at the task's is worse than an error.

The copy takes the directory's own files (a manifest names its stylesheet, never the script its own
`index.html` loads) plus every name a capability resolves, and applies the caps before the bytes are
read. It descends into no subdirectory, so a file the manifest does not point straight at stays in the
worktree and the served copy 404s it. A link among the directory's own files is skipped — a worktree
carries them; a link where the manifest says a file is is refused, because the served copy would
silently hold the wrong bytes.

`validateLoadedManifest` is the list an install runs, shared by both paths, so "it installed" and "it
loaded" cannot mean different things. A manifest that fails validation leaves the version already running
in place and answers with the failure text — the sentence the person relays to the agent. What it cannot
see it leaves alone: the files a manifest names but does not open (`css_url`, `mappings_file`,
`wasm_module`), an unknown injection point or permission, and which capability wins when a manifest
declares two executing ones. The image checks belong to the install path only, so a container type with
nothing to run fails at its first start; the skill's checklist is where an author is told to expect that.

A load that lands reloads the tab. An extension's frame, stylesheet and language pack take effect on a
fresh document and nothing hot-mounts them, so `TryExtension` reloads the way `services/app-update.ts`
does; only a refusal or a failure leaves the sentence on the bar to be relayed. That is also why the
button answers to Review: the version worth loading is the one a run has just finished writing. A reload
costs nothing the app cannot recover — the chat draft, the editor drafts and the offline queue are all
in IndexedDB.

Test: `frontend/src/lib/components/__tests__/review-bar-try-extension.unit.svelte.ts` over which task
offers the button, and the reload assertions in
`frontend/src/lib/composables/__tests__/try-extension.unit.svelte.ts`.

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

The dialog asks two questions about that project's sandbox before any of it exists: whether the agent's
container may reach the network, and whether it gets Docker's default capabilities. Both start off, and
the answer rides the create as `sandbox_network_isolation` and `sandbox_capabilities`. It has to: the
create starts a task, and `handleUpdateProject` refuses a sandbox change while one is running. An absent
field is the older answer — isolated, no capability — so nothing widens because a form was left alone.

The capability list is `DOCKER_DEFAULT_CAPABILITIES`
(`frontend/src/lib/components/SandboxConfigModal/capabilities.ts`), the one set the project's Sandbox
Configuration offers, so the two screens cannot disagree about what "Docker's defaults" means. A name is
shape-checked before the first directory is written and judged for what may be granted by
`types.ValidateSandboxAdditions` where the project row is made.

The person lands on the first task, not on its board: `docs/link-rows.md → A project the app just made`.

## The bundle

`templates/extension/` holds `README.md`, `AGENTS.md`, a `tests/README.md`, and the authoring skill
under `.agents/skills/extension-authoring/` — the project's own skill root, so the container binds it
read-only while the browser cannot reach it. The skill page decides the type and states what the load
refuses; `references/manifest.md` is the grammar and `references/lsp.md`, `references/agent.md` and
`references/llm.md` carry the fields of the types that cost an image or a module. Every JSON name the
manifest types declare has to appear in one of them.

## A UI extension that renders a file format the core does not know

The core has no format table: no viewer, no parser, no MIME decision for anything. What an author gets is one
declaration and one call, and `references/manifest.md` in the authoring skill holds both grammars.

- **Declare the suffix, then ask for the bytes.** `capabilities.ui.file_suffixes` (`["stl"]`, lowercase, no
  dot) is the whole grant: the host mounts the frame for a file whose suffix it names, and
  `GET /api/fs/read?kind=bytes` answers a file only when an enabled row declares its suffix. `fs.getBytes` is the
  capability that calls it; it is described in the install dialog but not flagged dangerous, because the
  mount names the file and the frame cannot name one.
- **The mount names the file, not the frame.** `extHost.fs.getBytes(side)` takes `'working'` or `'base'` and
  nothing else; project, task, path and base ref come from the mount context the person's screen built. A
  frame that could name a path would not need anyone to open anything.
- **The bytes arrive as a `Blob`.** The reply is its own frame (`{ type: 'bytes', id, byteLength, blob }`) and
  skips the JSON round-trip every other reply takes, so nothing encodes the file in either direction. Read it
  with `await blob.arrayBuffer()`, and parse heavy formats off the main thread — the host's own work is done
  before the reply and costs it one browser-held response.
- **The sanity check is the extension's.** A header claiming more content than `byteLength` holds is a
  request to allocate what cannot arrive, and no host-side rule can judge a format it has never heard of.
  Validate the header against the length before allocating from it. The host half is
  `docs/api-contract.md → The bytes an extension previews` and
  `docs/ui-extensions.md → Bytes Across the Channel`; this half has no test but yours.
- **What the host refuses, and how it says so.** A suffix no enabled row declared is 403, and a file over
  this server's file limit (`MIKE_FILE_SIZE_LIMIT_MB`) is 413. `.git` and anything outside the project-plus-task jail are refused too.
  Each refusal reaches the frame as a rejected promise carrying the server's sentence. Nothing in that list
  is format-aware.

The e2e fixture for the whole round trip is `frontend/tests/e2e/binary-preview.spec.ts`: it authors a manifest
declaring a made-up suffix, opens such a file, clicks Preview, and asserts the frame received the file's real
bytes.

## Tests

`go test ./backend/extensions/ ./backend/api/` is the gate over this whole subsystem:

- `dev_test.go` — the namespace, delete, listing, enable and repair guards.
- `dev_load_test.go` — loading, the three consumers resolving into the staging copy, the prune.
- `scaffold_test.go` — the bundle and the scaffolder. Every example manifest passes the load validator.
  Every JSON name the manifest types declare is named somewhere in the skill, walking into every
  capability type, so a field added to Go without a page fails. And no table in the grammar page names a
  field the types do not declare.
- `backend/api/extension_scaffold_test.go` — the create path, the defaults its first task arrives with,
  and the two sandbox answers: what an opt-in lands on the project row, what an absent field leaves alone,
  and the refusal that writes no directory. The dialog's steps and the fields it sends are
  `frontend/src/lib/components/ConfigModal/__tests__/extension-create-handoff.unit.svelte.ts`, and the
  address bar and the row it leaves behind are
  `frontend/tests/e2e/create-extension-opens-task.spec.ts`.
- `backend/api/project_dev_repo_test.go` — only a development project's directory is removed.
