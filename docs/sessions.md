# Sessions — Endpoint and UI Detail

This document describes the session management system. Security invariants are in `AGENTS.md → Sessions & Auth`.

## Storage Shape

A session is granted upon completion of a passkey ceremony (`docs/passkeys.md`). The `SessionManager` (`backend/auth/session.go`) handles the session lifecycle.

- **Tokens**: 32-byte random tokens, hex-encoded, with a 30-day TTL. The database stores the SHA-256 hash of the token (`token_hash`) to prevent session replay if the database is compromised.
- **Session ID**: A separate 8-byte random ID used for listing and revoking sessions. This ensures that listing sessions does not expose the session tokens.
- **Metadata**:
    - `ip` and `user_agent`: Recorded at session creation.
    - `last_seen`: Updated during validation, throttled to once every 60 seconds to reduce database writes.

## Endpoints

All `/api/sessions` routes are session-authenticated and CSRF-guarded.

- `GET /api/sessions`: Lists all live sessions (newest first). The response excludes tokens and CSRF fields to prevent leakage.
- `DELETE /api/sessions/{id}`: Revokes a session by ID.
- `POST /api/sessions/revoke-others`: Deletes all sessions except the caller's.

### Public Auth Endpoint
`GET /api/auth/session` provides session status and basic info without requiring authentication:
- **Status**: `authenticated`, `has_passkey`, `tls_ca_available`, and `csrf_token`.
- **Device Name**: The `device_name` is derived from the requesting browser's User-Agent, which the login form uses to pre-fill the device name field.

## Client Implementation

The `frontend/src/lib/api/auth.ts` module manages authentication:
- Handles `checkSession`, passkey ceremonies, logout, and pairing code management.
- Stores the CSRF token.
- Uses the `GET /api/auth/session` response to decide whether to show the passkey or pairing code login form.

## UI

The **Sessions** section in Settings $\to$ **About** (`ConfigModal/SessionsSection.svelte`) allows users to:
- View active sessions with relative timestamps.
- Revoke individual sessions or sign out of all other sessions.
- These actions are applied immediately, bypassing the modal's Save/Cancel flow.

The **API tokens** section manages a separate, narrower credential used for specific API routes (`docs/api.md`).

## Verification

- **Backend Tests**: `backend/store/sessions_test.go`, `backend/auth/session_test.go`, `backend/server/handler_sessions_test.go`, `backend/types/idgen_test.go`.
- **Frontend Tests**: `frontend/src/lib/api/__tests__/sessions.unit.ts`, `frontend/src/lib/api/__tests__/auth.unit.ts`.
- **E2E Tests**: `settings-sessions.spec.ts`.

`go test ./backend/auth/` verifies the hashed session and store logic. `npx playwright test settings-sessions` verifies the UI and revocation flow.
