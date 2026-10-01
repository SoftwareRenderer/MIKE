# The HTTP API for a bearer token

Read this when you are writing something that drives MIKE from outside the browser — a script, a
CI job, another agent. It is the client-facing half: how a token is provisioned, which routes it
opens, and what those routes do. `docs/api-contract.md` is the internal contract (frame shapes,
route-table exemptions, error vocabulary) and `AGENTS.md → API` the rules; a route change edits
that file and re-checks the scope table here.

## Provisioning a token

**Settings → About → API tokens.** Type a name for the token (what uses it — the label is the only
thing the screen shows afterwards), press **Create token**, and copy the value from the panel that
appears.

- **The value is shown once.** Only `hex(sha256(token))` is stored, so the row cannot be replayed as
  a credential if the database is copied, and this server cannot tell you the value again. Lost it,
  revoke the row and issue another.
- The token looks like `mike_` followed by 64 hex characters. The prefix is stable: use it to teach
  a secret scanner what yours are.
- **It does not expire.** Revoking the row is the control, and it takes effect on the token's very
  next request — there is no cache to wait out.
- A row shows **Last used**, stamped at most once a minute (the same throttle a login session's
  `last_seen` uses, because the store serialises on one connection). Revoked rows are gone; the
  listing holds labels and timestamps only, never a credential.
- Everything on this screen is session-cookie work. **A token cannot list, create or revoke
  tokens** — that is deliberate, so a leaked token cannot promote itself.

The mechanism is `backend/auth/api_tokens.go` (issue and validate), `backend/server/token_auth.go`
(the route allowlist), `backend/server/handler_tokens.go` (the three management routes).

## Presenting it

```
Authorization: Bearer mike_1f4c…
```

- Only the `Authorization` header. A token in a query string would land in every proxy log and
  browser history between you and the server, so no route reads one.
- The scheme is case-insensitive (RFC 7235); the secret is not.
- The header is authoritative. A request that presents a token is never served by a session cookie
  it happens to travel with: an unknown or revoked token answers **401** (`WWW-Authenticate: Bearer`)
  rather than quietly acting as whoever's cookie arrived.
- TLS is on by default and the server signs its own certificate; a client that is not on the
  machine needs the local CA from Settings → About (**Download CA Certificate**).
- Two guards still apply to every call, cookie or not: the **Host** must name the server (loopback,
  the address it listens on, or an entry of `MIKE_ALLOWED_HOSTS`) or the request is refused **421**;
  and a request body over 32 MB is refused **413** before it is read.
- No CSRF header is needed or consulted with a token. CSRF exists because a browser attaches a
  session cookie by itself; a token reaches the server only because your code put it in the header.

## What a token reaches

The whole list. Anything not here answers **403** — the credential is live, the route is not
grantable to it. `tokenScope` in `backend/server/token_auth.go` is the same table, and
`TestTokenScopeIsTheApprovedSet` fails when the two drift.

| Method | Path | |
|---|---|---|
| `GET` | `/api/projects` | list projects |
| `GET` | `/api/projects/{projectID}` | read one project |
| `GET` | `/api/projects/{projectID}/tasks` | list a project's tasks |
| `POST` | `/api/projects/{projectID}/tasks` | create a task |
| `GET` | `/api/projects/{projectID}/tasks/{taskID}` | read one task |
| `PUT` | `/api/projects/{projectID}/tasks/{taskID}` | update a task — `status` included |
| `DELETE` | `/api/projects/{projectID}/tasks/{taskID}` | delete a task and its subtree |
| `POST` | `/api/projects/{projectID}/tasks/{taskID}/start` | start a task |
| `POST` | `/api/projects/{projectID}/tasks/{taskID}/start-subtask` | start a task whose parent is already running |
| `POST` | `/api/projects/{projectID}/tasks/{taskID}/stop` | pause a task and every descendant that could still run |

Not on it, among others: `/api/config`, `/api/sessions`, `/api/tokens`, the file routes
(`/api/fs…`), the git and diff routes, task messages, threads, uploads, `/send`, `/commit`,
`/resume`, `/summarize`, `/compact`, `…/move`, `DELETE …/tasks/done`, creating or deleting a
project, extensions, previews, terminals, and both WebSocket upgrades (`/api/stream`, `/api/lsp`).

`…/move` is absent because it is not needed: `PUT …/tasks/{id}` writes `status`, which is what
moving a card between columns is. The WebSockets are absent because a browser is the only thing that
cannot put a header on a WebSocket, and a script is not a browser.

## Reading a task

Every task route answers a `task` object. The fields worth branching on:

| Field | |
|---|---|
| `id`, `project_id` | the pair every task route takes in its path |
| `title`, `prompt` | the card's heading (generated from the prompt when not set) and the instruction the agent starts with |
| `status` | the board column: `backlog` · `in_progress` · `review` · `done` |
| `execution_state` | `idle` · `running` · `pulling_image` · `paused` · `completed` · `failed` |
| `output` | the agent's last reported result text |
| `branch`, `base_ref`, `base_commit`, `branch_tip` | the task's own branch and the commits its diff is measured between |
| `worktree_exists` | whether its working directory is on disk |
| `parent_task_id` | set for a subtask |
| `operating_mode` | `act` · `plan` · `explore` |
| `auto_commit`, `auto_merge`, `subtasks_auto_merge` | what a finished run does with its work |
| `has_merge_conflict` | the run is waiting for a human about a conflict |
| `session_cost` | USD spent by the last run |
| `created_at`, `updated_at`, `moved_at` | RFC3339 UTC; `moved_at` is the last column change |

**`status` is not the answer to "is it still running".** A stopped task keeps `status = in_progress`
and moves `execution_state` to `paused`. Poll those two together:

```bash
jq -r '"\(.status) \(.execution_state)"' <<<"$task"
```

## The routes

### `GET /api/projects`

Every project: `[{ id, name, path, origin_branch, sandbox_enabled, sandbox_memory_limit,
created_at, updated_at, … }]`. This is where a client gets a `projectID`.

### `GET /api/projects/{projectID}`

One project, same shape. `404` when the id names nothing.

### `GET /api/projects/{projectID}/tasks`

Every task of the project, as `[{ task }, …]` — no paging, no column filter.

### `POST /api/projects/{projectID}/tasks` → `201`

Sends the fields you want and reads the row back. The ones a headless caller normally sends:

```json
{
  "prompt": "Add a --json flag to the CLI and cover it with tests",
  "title": "CLI --json flag",
  "parent_task_id": "optional-parent",
  "operating_mode": "act",
  "auto_commit": true,
  "auto_merge": false,
  "model_name": "…",
  "llama_server_url": "…"
}
```

- `prompt` is what the agent is told; omit `title` and one is generated from it (that generation is
  a model call, so `title_generating` is `true` on the answer and the title arrives later).
- Anything you leave out takes the server's task defaults, including the model and the merge flags.
  Anything you send wins.
- Unknown `operating_mode`, an unknown `status`, or a `parent_task_id` that is not a task of this
  project is a `400` naming the problem, and so is a body carrying `plan_mode`: the answer names
  `operating_mode`, which is the field that decides. The response is the created task, with its `id`.

```bash
curl -sS -X POST -H "Authorization: Bearer $MIKE_TOKEN" -H 'Content-Type: application/json' \
  -d '{"prompt":"Triage the open lint failures"}' \
  https://localhost:3000/api/projects/$PROJECT/tasks
```

### `GET` / `PUT` / `DELETE /api/projects/{projectID}/tasks/{taskID}`

`GET` answers the task. `PUT` is a **partial update**: send only the keys you want written, and a
key you omit is left alone (`docs/api-contract.md → Partial updates`). `"status": "review"` moves
the card; `"title": ""` clears the title, because present means write. A wrong type is a `400`
naming the field, and validation runs before anything is written. `DELETE` answers `204` and takes
the task's subtasks with it.

Both `{projectID}` and `{taskID}` must agree: a task belonging to another project is a `404`, not
 somebody else's row.

### `POST …/start`

Starts the task: it creates the worktree and branch if they do not exist, so there is nothing to
call first, and returns the task it started. On a parent with unfinished subtasks it starts a
subtask instead and answers **that** task — read `id` off the response rather than assuming it is
the one you named. `409` means it is already running, with the reason in the body.

### `POST …/start-subtask`

The same start for a task whose parent is already in progress. A subtask created under a running
parent lands `in_progress` / `idle` — waiting, not running — and this is the route that runs it.

### `POST …/stop`

Pauses the task and every descendant that could still run: a run in the branch is cancelled, and a row
that was waiting to run reads `paused`, so nothing under it starts again until a person starts it. Rows
in Backlog, Review or Done are left where they are. Answers the task. Stopping is the result, so it
completes whatever the client does afterwards.

## Watching a run

There is no streaming endpoint for a token: `/api/stream` is a WebSocket and stays session-only.
Poll `GET …/tasks/{taskID}` — `execution_state` is what moves while a run goes (`running`, then
`completed`, `failed` or `paused`), and `output` carries what the agent reported. There is
deliberately no timeout on this server: a long wait is a long wait, so give your client no deadline
for it, and treat a lost connection as "keep asking" rather than "it failed".

## Errors

| Status | Meaning |
|---|---|
| `400` | the body is wrong — invalid JSON, an unknown column name, a field of the wrong type. The sentence names the field |
| `401` | no valid bearer token. Retry with a live credential; a second try with the same one will not help |
| `403` | a live token on a route it does not cover. Nothing about the token is wrong |
| `404` | no such project, task, or path — including a task that exists under a different project |
| `405` | right path, wrong verb; the response carries `Allow` |
| `409` | the action conflicts with reality: a task already running, a sandbox the user must reconfigure |
| `413` | request body over 32 MB |
| `421` | the `Host` header names a different authority than this server (`MIKE_ALLOWED_HOSTS`) |
| `429` | too many login attempts — the password endpoint only; token calls are not rate-limited, because a 256-bit secret is not guessable |
| `500` | this server's fault. The body is MIKE's own sentence and the cause is in its log, not in your response |

A server fault is reported as MIKE's sentence on purpose: the cause would be git's stderr, a driver
message or an absolute path into someone's working directory.

## Tests that hold this surface

`backend/store/api_tokens_test.go`, `backend/auth/api_tokens_test.go`,
`backend/server/handler_tokens_test.go`, `backend/server/token_auth_test.go` (the allowlist, in both
directions), `frontend/src/lib/api/__tests__/tokens.unit.ts`, and the e2e
`frontend/tests/e2e/settings-api-tokens.spec.ts`.
## Checking this

`go test ./backend/auth/ ./backend/server/` covers issuance, the route allowlist and what a bearer token
is allowed to answer for.
