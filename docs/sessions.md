# Sessions — Endpoint and UI Detail

Read this when changing `/api/sessions*`, `backend/auth/session.go`, `backend/store/` session
rows, or Settings → About → Sessions. The security invariants live in
`AGENTS.md → Sessions & Auth`.

## Storage shape

A session is what a finished passkey ceremony earns (`docs/passkeys.md`), so `SessionManager`
(`backend/auth/session.go`) holds **no credential of its own** — nothing here can be compared against a
password, and `StartSession` is reachable only from a route whose ceremony verified.

**Tokens:** 32 bytes from `crypto/rand`, hex-encoded, 30-day TTL. The raw token exists only in the
response/cookie; the DB stores `hex(sha256(token))` and looks a session up by that digest alone, so a copy
of `kanban.db` cannot be replayed as a live session. The same digest is written to `token_hash` (the index
the lookup walks) and to `token`, whose `NOT NULL UNIQUE` constraint it therefore guards.

**Session id:** 8 bytes, drawn from the same `crypto/rand` call as the token and the CSRF token but from
bytes neither carries. `GET /api/sessions` lists ids and `DELETE /api/sessions/{id}` revokes by id, so an
id that was a slice of the token would put part of a live credential in every listing. A collision on 8
bytes is answered by the insert failing, not by reuse.

**Per-row metadata:** `ip` + `user_agent` are recorded when the ceremony opens the session. `last_seen`
is stamped during validation but throttled to one write per 60 s (`lastSeenInterval`) — the store
serialises on a single connection, so an unthrottled write would turn every authenticated request into a
DB write.

## Endpoints

`backend/server/handler_sessions.go`, routes in `routes.go`. Every one is session-authenticated
(`SessionMiddleware`) and every non-GET is CSRF-guarded; no `/api/sessions` path is ever in
`publicPaths`.

- `GET /api/sessions` → all live sessions, newest first: `{id, created_at, last_seen, expires_at,
  ip, user_agent, current}` (RFC3339 UTC; `""` for a never-seen or pre-migration row). The response
  type has **no token / token-hash / CSRF field at all**, so a credential cannot leak by accident.
- `DELETE /api/sessions/{id}` → `204` (`404` when unknown). Revoking your *own* session is allowed:
  the next request 401s and the SPA falls back to the login modal, where signing in again is a passkey
  assertion.
- `POST /api/sessions/revoke-others` → `{"revoked": n}`; deletes every session except the caller's.

`GET /api/auth/session` is the public sibling of these. It answers `authenticated`, `has_passkey`,
`tls_ca_available` and `device_name`, plus `csrf_token` when a session exists. `tls_ca_available` is `localCAvailable()`, the
same decision the CA download and the `/api/config` row are made of, so a link the login form shows cannot
point at a file the server would refuse to serve. `device_name` is about the browser asking rather than about
any credential: what its own User-Agent reads as, which is the name the pairing form opens its field with
(`docs/passkeys.md → How it is held`).

**`/api/auth/*` and `/api/auth/passkeys*` have one client:** `frontend/src/lib/api/auth.ts` holds
`checkSession`, the two ceremony calls (`registerPasskeyWithCode`, `signInWithPasskey`), `logout`, the
passkey list/rename/revoke pair and the mint/revoke of pairing codes — and it is the only place the CSRF
token is stored. Its three unusual readings are what the boot path and the login form depend on. A session
answer is read from the body whatever the status, so a signed-out browser is never read as an outage. The
same answer's `has_passkey` decides whether the form asks for a passkey or for a pairing code, and its
`tls_ca_available` (absent reads as false) whether that second body may link the CA download. Its
`device_name` (absent reads as blank) is what that body's device-name field starts with.
A logout that 401s settles: revoking the current session signs out behind it, and the caller's teardown
must still run.

## UI

The **Sessions** section of Settings → **About** (`ConfigModal/SessionsSection.svelte`, API in
`$lib/api/sessions.ts`) lists the rows (own session pinned on top, relative timestamps with
absolute-time tooltips), with per-row **Revoke** and a **Sign out all other sessions** action. It
mutates immediately — deliberately **outside** the modal's dirty/Save/Cancel flow, like the PWA
install button above it — and fetches only when the About tab mounts. Revoking the current session
hands off to the modal's `onSignOut` prop.

The **API tokens** section under it (`ConfigModal/ApiTokensSection.svelte`, `$lib/api/tokens.ts`) is
a different credential with a smaller reach: login sessions authenticate the whole app, a token
authenticates the routes listed in `docs/api.md`. Above it, **Devices & passkeys**
(`ConfigModal/PasskeysSection.svelte`) manages the credentials that open a session at all —
`docs/passkeys.md`.

## Tests

`backend/store/sessions_test.go`, `backend/auth/session_test.go`,
`backend/server/handler_sessions_test.go`, `backend/types/idgen_test.go`,
`frontend/src/lib/api/__tests__/sessions.unit.ts`, `frontend/src/lib/api/__tests__/auth.unit.ts`, e2e
`settings-sessions.spec.ts` (isolated).
## Checking this

`go test ./backend/auth/` covers the hashed session and the token store; the ceremony that opens a session
is measured by the check in `docs/passkeys.md`. `cd frontend && npx playwright test settings-sessions`
drives Settings → About, where a session is listed and revoked.
