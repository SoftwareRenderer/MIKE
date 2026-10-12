# The HTTP API for a bearer token

Use this guide when controlling MIKE from outside the browser, such as with a script, CI job, or another agent.
It covers token provisioning, available routes, and their functions. `docs/api-contract.md` is the internal
contract (frame shapes, route-table exemptions, error vocabulary) and `AGENTS.md → API` the rules; a route
change edits that file and re-checks the scope table here.

## Provisioning a token

**Settings → About → API tokens.** Type a name for the token (what uses it — the label is the only
thing the screen shows afterwards), press **Create token**, and copy the value from the panel that
appears.

- **The token value is shown only once.** We store only the hash (`hex(sha256(token))`), so the server
  cannot retrieve the original value and a copied database replays no credential. Lost it,
  revoke the row and issue another.
- Tokens start with `mike_` followed by 64 hex characters. This stable prefix helps secret scanners
  identify them.
- **Tokens do not expire.** Revoke the token to disable it; the change takes effect on the token's very
  next request — there is no cache to wait out.
- The **Last used** timestamp is updated at most once per minute to reduce database load. Revoked rows
  are gone; the listing holds labels and timestamps only, never a credential.
- Management of tokens requires a session cookie. **A token cannot list, create or revoke tokens** —
  that is deliberate, so a leaked token cannot promote itself.
- Bearer tokens cannot pair devices or manage passkeys, as these routes require session cookies and
  CSRF protection (`POST`/`DELETE /api/auth/pairing`, `GET`/`PATCH`/`DELETE /api/auth/passkeys`), so a
  stolen token cannot take the whole account.

Implementation: `backend/auth/api_tokens.go` (issuance and validation), `backend/server/token_auth.go`
(the route allowlist), and `backend/server/handler_tokens.go` (the three management routes).

## Presenting it

```
Authorization: Bearer mike_1f4c…
```

- Use only the `Authorization` header. Tokens are not accepted in query strings to avoid leaking them
  in proxy logs or browser history.
- The scheme is case-insensitive (RFC 7235); the secret is not.
- The `Authorization` header takes precedence. If a token is provided, the server ignores any session
  cookies. An invalid or revoked token returns **401** (`WWW-Authenticate: Bearer`), even if a valid
  session cookie is present.
- TLS is enabled by default. Clients outside the host machine must install the local CA certificate
  available in Settings → About (**Download CA Certificate**).
- Every request must pass two checks: the **Host** header must match the server (loopback, the address
  it listens on, or an entry of `MIKE_ALLOWED_HOSTS`) or the request is refused **421**. And a request
  body over 32 MB is refused **413** before it is read.
- Bearer tokens do not require CSRF headers because they are explicitly added by the client, unlike
  session cookies which are automatically attached by browsers.

## What a token reaches

Tokens have access to all routes listed below. Any other route returns **403** — the credential is
live, but the route is not grantable to it. `tokenScope` in `backend/server/token_auth.go` is the same
table, and `TestTokenScopeIsTheApprovedSet` fails when the two drift.

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
`/resume`, `/summarize`, `/compact`, `…/move`, `DELETE …/tasks/done`, and creating or deleting a project.
Extensions, previews, terminals and both WebSocket upgrades (`/api/stream`, `/api/lsp`) are outside it too.

`…/move` is unnecessary because `PUT …/tasks/{id}` can update the `status` to move a card between
columns. WebSockets are not supported for bearer tokens because standard WebSocket clients cannot
easily set custom headers.

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

**`status` does not indicate if a task is running.** A paused task remains `status = in_progress`
and moves `execution_state` to `paused`. Poll both fields together:

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

Bearer tokens cannot use the streaming endpoint (`/api/stream`), which is session-only. Instead, poll
`GET …/tasks/{taskID}` — `execution_state` is what moves while a run goes (`running`, then
`completed`, `failed` or `paused`), and `output` carries what the agent reported. The server has no
timeout; clients should handle long waits and connection losses by retrying.

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
| `429` | too many login attempts — the passkey ceremony and pairing-code routes only; token calls are not rate-limited, because a 256-bit secret is not guessable |
| `500` | this server's fault. The body is MIKE's own sentence and the cause is in its log, not in your response |

Server faults return a generic error message to avoid leaking sensitive information like system paths or driver errors.

## Tests that hold this surface

`backend/store/api_tokens_test.go`, `backend/auth/api_tokens_test.go`,
`backend/server/handler_tokens_test.go`, `backend/server/token_auth_test.go` (the allowlist, in both
directions), `frontend/src/lib/api/__tests__/tokens.unit.ts`, and the e2e
`frontend/tests/e2e/settings-api-tokens.spec.ts`.
## Checking this

`go test ./backend/auth/ ./backend/server/` covers issuance, the route allowlist and what a bearer token
is allowed to answer for.
