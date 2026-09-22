# Sessions — Endpoint and UI Detail

Read this when changing `/api/sessions*`, `backend/auth/session.go`, `backend/store/` session
rows, or Settings → About → Sessions. The security invariants live in
`AGENTS.md → Sessions & Auth`.

## Storage shape

`SessionManager` (`backend/auth/session.go`) keeps **only `sha256(password)`** and compares digests
with `subtle.ConstantTimeCompare` — the plaintext never outlives the login request, and both sides
are always 32 bytes (a length mismatch would make the constant-time compare short-circuit and leak).

**Tokens:** 32 bytes from `crypto/rand`, hex-encoded, 30-day TTL. The raw token exists only in the
response/cookie; the DB stores `hex(sha256(token))` (indexed, in both `token_hash` and the legacy
`token` column), so a copy of `kanban.db` cannot be replayed as a live session.
`BackfillSessionTokens` migrates pre-hash rows once at startup (their legacy column gets a
`revoked-<id>` placeholder).

**Session id:** 8 bytes, drawn from the same `crypto/rand` call as the token and the CSRF token but from
bytes neither carries. `GET /api/sessions` lists ids and `DELETE /api/sessions/{id}` revokes by id, so an
id that was a slice of the token would put part of a live credential in every listing. A collision on 8
bytes is answered by the insert failing, not by reuse.

**Per-row metadata:** `ip` + `user_agent` are recorded at login; `last_seen` is stamped during
validation but throttled to one write per 60 s (`lastSeenInterval`) — the store serialises on a
single connection, so an unthrottled write would turn every authenticated request into a DB write.

## Endpoints

`backend/server/handler_sessions.go`, routes in `routes.go`. Every one is session-authenticated
(`SessionMiddleware`) and every non-GET is CSRF-guarded; no `/api/sessions` path is ever in
`publicPaths`.

- `GET /api/sessions` → all live sessions, newest first: `{id, created_at, last_seen, expires_at,
  ip, user_agent, current}` (RFC3339 UTC; `""` for a never-seen or pre-migration row). The response
  type has **no token / token-hash / CSRF field at all**, so a credential cannot leak by accident.
- `DELETE /api/sessions/{id}` → `204` (`404` when unknown). Revoking your *own* session is allowed:
  the next request 401s and the SPA falls back to the login modal.
- `POST /api/sessions/revoke-others` → `{"revoked": n}`; deletes every session except the caller's.

## UI

The **Sessions** section of Settings → **About** (`ConfigModal/SessionsSection.svelte`, API in
`$lib/api/sessions.ts`) lists the rows (own session pinned on top, relative timestamps with
absolute-time tooltips), with per-row **Revoke** and a **Sign out all other sessions** action. It
mutates immediately — deliberately **outside** the modal's dirty/Save/Cancel flow, like the PWA
install button above it — and fetches only when the About tab mounts. Revoking the current session
hands off to the modal's `onSignOut` prop.

The **API tokens** section under it (`ConfigModal/ApiTokensSection.svelte`, `$lib/api/tokens.ts`) is
a different credential with a smaller reach: login sessions authenticate the whole app, a token
authenticates the routes listed in `docs/api.md`.

## Tests

`backend/store/sessions_test.go`, `backend/auth/session_test.go`,
`backend/server/handler_sessions_test.go`, `backend/types/idgen_test.go`,
`frontend/src/lib/api/__tests__/sessions.unit.ts`, e2e `settings-sessions.spec.ts` (isolated).
## Checking this

`go test ./backend/auth/` covers the digest comparison, the hashed session and the token store.
`cd frontend && npx playwright test settings-sessions` drives Settings → About, where a token is issued.
