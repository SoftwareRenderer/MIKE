# Passkeys — Login, Pairing, and the Reset

This owns how a browser proves who it is: the WebAuthn registration a pairing code authorizes, the
assertion every later sign-in is, the pairing codes and ceremony tickets both ceremonies run on, and
`mike --reset-passkeys`. It does not own the session a finished ceremony opens (`docs/sessions.md`), the
bearer token that reaches a narrower set of routes (`docs/api.md`), or the Host pinning that bounds which
hostname a credential may be bound to (`docs/tls.md`).

**A pairing code authorizes one registration and authenticates nothing.** No session, no cookie and no
CSRF token exists on the strength of a code: the code is spent before the ceremony's options are issued.
The session is opened only as the answer to a credential response the device's own authenticator signed. That is what makes the code safe to print on a terminal and put in a log — once it is spent,
there is nothing left to steal.

## What must stay true

- **A session opens only as the answer to a finished ceremony.** Both ceremony finishes end at
  `openSession`, and nothing else on these routes writes the session cookie. A route that opens a session
  on any other condition has turned the pairing code into a password.
- **One code spends once, and it is spent at register-begin** — before any options exist. A code that
  could begin twice could pair twice.
- **A code never becomes a log line, a query string, or a child process's environment.** It is printed
  once (the only place it is written), reaches a browser through the URL fragment, and lives in memory.
  A secret with no on-disk copy cannot leak from a backup.
- **The relying-party id and origin are the request's**, and each credential stores the rpID it was minted
  for. An assertion offers only that rpID's credentials, so a key minted at `localhost` is never offered
  at a proxy domain.
- **A bare IP address is refused as a relying party** — at the ceremony routes and in the boot banner.
  `protocol.ValidateRPID` accepts a hostname only, so an address has no credential to offer and the
  answer says so instead of failing inside the browser's prompt.
- **User verification is required.** A roaming key needs a PIN or biometric: touch proves presence, and
  presence alone does not open a session.
- **The last remaining passkey cannot be revoked from the browser.** The GUI's refusal (409, naming
  `mike --reset-passkeys`) is what keeps a one-device install reachable.
- **`--reset-passkeys` is one-shot, deletes sessions too, and refuses a live server.** A session can mint
  a pairing code, so a reset that left sessions alive would not revoke the device it claims to.
- **Ceremony state crosses the gap between `begin` and `finish` only inside a sealed ticket** carrying the
  ceremony's purpose. A registration ticket must fail at the sign-in route.
- **The stored credential is the verifier's own record, written back by every assertion** — the signature
  counter and the latched flag bits move there and nowhere else.
- **Pairing and passkey management are session-authenticated, CSRF-guarded, and out of `tokenScope`.** A
  leaked bearer token must not be able to hand out the right to register a passkey.

## How it is held

**The ceremonies** (`backend/auth/passkey.go`). `RelyingHost` is the (rpID, origin) pair `NewRelyingHost`
derives from a request authority plus `requestIsHTTPS`; it returns `ErrHostCannotHostPasskeys` for an
address. `verifier()` builds a `webauthn.Config` per request, because one config shared between
`localhost` and a proxy name would accept a signature made for one as proof of the other.
`Passkeys.BeginRegistration` excludes the credentials already registered; `FinishRegistration` stores the
row with the rpID it was minted for and `FinishAssertion` writes the record back. A
`CloneWarning` on a credential that cannot be synchronized (`Flags.BackupEligible` false) is a
`ErrCeremonyRefused`; on a synchronized one it is a warning, because the person has just unlocked a
phone whose counter means nothing across devices. `EncodeCredentialID` is the one base64url spelling of a
credential id — column, listing, and path parameter.

**Pairing codes** (`backend/auth/pairing.go`). `PairingCodes.Mint` draws eight characters
(`crypto/rand`, redrawn above the alphabet's largest multiple so no character is likelier than another)
from a look-alike-free alphabet and keeps only `hex(sha256(code))`. `Consume` deletes the entry before it
checks the expiry, so a spend is a spend. Unknown, expired and spent are one error —
`ErrInvalidPairingCode` — because telling them apart tells a guesser how close they came.
`NormalizePairingCode` is the one shape a typed code takes, mirrored by `normalizePairingCode`
(`frontend/src/lib/utils/login-flow.ts`) so the form's own button agrees with what the server accepts.
TTLs: `BootPairingTTL` for the printed code, `DevicePairingTTL` for one minted from Settings, at most
`pairingMaxLive` live at once. There is no per-code attempt counter on purpose: a wrong code has no entry
to count against, so the login rate limiter on the route (`MIKE_LOGIN_RATE_LIMIT`) is the bound.

**Tickets** (`backend/auth/ticket.go`). `Tickets.Seal`/`Open` carry the library's `SessionData` across the
two requests: HMAC-SHA256 over purpose + payload, keyed by HKDF over `MIKE_ENCRYPTION_KEY`, bounded by
`TicketTTL` and `ticketMaxPayloadBytes`. A table would be state held for a client that closed the tab —
an abandoned ceremony is the normal outcome — and the purpose inside the MAC, rather than inside the
payload, is what stops one ceremony's ticket being presented to the other.

**Presentation** (`backend/auth/pairing_qr.go`, `backend/server/boot_pairing.go`). `PairingURL` puts the
code in the fragment. `PairingQRPNG` is the Settings picture, `PairingQRTerminal` the boot picture
(ANSI half-blocks, two module rows per text line). `pairAtBoot` mints and prints only while
`PasskeyCount()` is zero, so an unattended restart never announces a live code into a log nobody is
reading, and warns when the listen address is not loopback or the printed host cannot hold a passkey.
`logging.Banner` writes the QR to the same sink as the log lines without prefixing each of its rows.

**Storage** (`backend/store/passkeys.go`). Its own DDL beside its accessors, one row per device, `record`
a blob. `DeletePasskey` returns `ErrLastPasskey` for the final row; `ResetPasskeys` deletes passkeys and
sessions inside `Store.tx`.

**The HTTP surface** (`backend/server/handler_passkey.go`, routes in `routes.go`). The four ceremony
routes are public and rate-limited; `POST`/`DELETE /api/auth/pairing`, `GET`/`PATCH`/`DELETE
/api/auth/passkeys` are not. `refuseCeremony` is where a ceremony refusal chooses its status, and every
other error goes through `httputil.Fault` with the cause left in the log. `cleanPasskeyName` is the one
rule for a device label; `deviceLabel` is what an unnamed device is called, read off the User-Agent.

**The one-shot reset** (`main.go`). `resetPasskeys` resolves config, refuses a database that is not
there, refuses an address that answers, then wipes and exits — before any listener, trace file or
container sweep exists. `--reset-passkeys` has no environment spelling on purpose: a credential-destroying
command that a stale environment variable selects runs at every restart.

**The browser side.** `$lib/utils/webauthn.ts` converts between the ceremony's JSON and the `ArrayBuffer`s
`navigator.credentials` insists on, and `passkeySupport()` names why a ceremony cannot run here
(`insecure-context` is the one the person can act on). `$lib/utils/login-flow.ts` owns every branch:
no passkey means the code field, a passkey means the assertion, and a cancelled prompt is a note rather
than a fault. `LoginModal.svelte` paints the stage it is given and takes a `#pair=CODE` fragment out of
the address bar as soon as the field holds it. `ConfigModal/PasskeysSection.svelte` is the management
surface: list, rename, revoke, mint, and cancel the outstanding codes.

**The test seam.** `backend/testutil/testwebauthn` builds a real ES256 credential and both ceremony
bodies, so registration, assertion, wrong origin, missing user verification, counter regression and a
foreign challenge are tested with genuine signatures and no browser. The e2e lane drives a Chromium
virtual authenticator instead (`docs/testing.md → Passkeys in the e2e lane`).

## Checking this

`go test ./backend/auth/ ./backend/store/ ./backend/server/` runs the ceremonies (real signatures), the
rpID scoping, the single-use code, the ticket's purpose and expiry, the last-passkey refusal, the atomic
reset, and every route refusal above. `make test-e2e SPECS=login` walks a browser through pairing,
registration and assertion; `make test-e2e SPECS=settings-passkeys` drives the Settings surface, including
the minted code a second device pairs with.
