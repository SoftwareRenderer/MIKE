# Passkeys — Login, Pairing, and the Reset

This document describes how the app handles authentication using WebAuthn: registration via pairing codes, sign-in via assertions, and the `--reset-passkeys` command.

## Overview

A **pairing code** authorizes a one-time registration. It does not authenticate a user or open a session. A session is only opened after a device's authenticator signs a challenge (assertion) or completes a registration ceremony.

## Security Invariants

- **Session Opening**: A session opens only after a completed registration or assertion ceremony. No other route writes the session cookie.
- **Pairing Code Lifecycle**: One code equals one passkey. A code is spent when the passkey is written. If registration fails, the code is restored.
- **Zero-Leakage**: Pairing codes are never logged, passed in query strings, or stored in environment variables. They are transmitted via URL fragments and kept in memory.
- **Host Pinning**: Credentials are bound to the Relying Party ID (rpID) and origin of the request. A key minted at `localhost` cannot be used at a proxy domain.
- **IP Refusal**: Bare IP addresses are refused as relying parties.
- **User Verification**: PIN or biometric verification is required for roaming keys.
- **Last Passkey Protection**: The last remaining passkey cannot be revoked from the browser to prevent lockout.
- **Reset Logic**: `--reset-passkeys` is a one-shot command that deletes all passkeys and sessions. It refuses to run if the server is active.
- **Ticket Isolation**: Ceremony state is passed between `begin` and `finish` requests using sealed tickets. A registration ticket cannot be used at a sign-in route.
- **Credential Updates**: The stored credential record is updated by every assertion (e.g., signature counter).
- **Privilege**: Pairing and passkey management require session authentication and CSRF protection; they are excluded from `tokenScope`.

## Implementation Details

### Ceremonies (`backend/auth/passkey.go`)
Handles the WebAuthn flow. It derives the `RelyingHost` (rpID, origin) from the request. Each request gets a unique `webauthn.Config` to prevent cross-host signature acceptance.

### Pairing Codes (`backend/auth/pairing.go`)
- **Minting**: Generates an 8-character code from a look-alike-free alphabet. Only the SHA-256 hash is stored.
- **Spending**: A code is "spent" (deleted) upon successful registration.
- **TTLs**: Different TTLs for boot-time and Settings-minted codes.
- **Rate Limiting**: Bound by `MIKE_LOGIN_RATE_LIMIT`.

### Tickets (`backend/auth/ticket.go`)
Transports ceremony state between requests. Tickets are HMAC-SHA256 signed using `MIKE_ENCRYPTION_KEY` and have a limited TTL and payload size.

### Presentation & UI
- **Pairing**: Codes are provided in URL fragments. The boot banner prints the code in a specific frame for
  terminal visibility, with one URL per authorized host — Host Pinning above means any name the operator
  pinned in `--allowed-hosts`/`MIKE_ALLOWED_HOSTS` is an address a person may pair at. With none configured
  the listen address stands in, resolved to `localhost` when it names no host. An authorized address is
  printed with the rest, and a warning names it. With several URLs on screen the banner says a passkey
  belongs to the address it was paired at.
- **QR Codes**: Settings provides a PNG QR code for pairing links.
- **Browser Flow**: `$lib/utils/login-flow.ts` manages the transition between the code field and passkey assertion.
- **Management**: `ConfigModal/PasskeysSection.svelte` allows renaming and revoking devices.

### Storage (`backend/store/passkeys.go`)
Stores one row per device. `ResetPasskeys` deletes all records within a transaction.

### Service Worker & Public Access
`/sw-notifications.js` is public because the worker fetches it without a session.

## Verification

### Backend Tests
`go test ./backend/auth/ ./backend/store/ ./backend/server/` tests:
- Registration and assertion with real signatures.
- rpID scoping and user verification.
- Concurrent registration attempts.
- Ticket expiry and purpose validation.
- Atomic reset and route refusals.

### E2E & Frontend Tests
- `make test-e2e SPECS=login`: Verifies the full pairing and login flow.
- `make test-e2e SPECS=settings-passkeys`: Verifies device management and pairing.
- `login-flow.unit.ts` & `app-shell-copy.unit.svelte.ts`: Verify the browser-side flow.
- `TestPairingBannerPrintsOneRowPerAuthorizedHost` and `TestBootPairingPrintsAPairingURLForEveryAuthorizedHost` hold the banner's URL rows. `TestBootPairingURLNamesAHostThatCanCarryACredential` holds which address each URL names.
