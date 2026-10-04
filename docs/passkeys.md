# Passkeys — Login, Pairing, and the Reset

This owns how a browser proves who it is: the WebAuthn registration a pairing code authorizes, the
assertion every later sign-in is, the pairing codes and ceremony tickets both ceremonies run on, and
`mike --reset-passkeys`. It does not own the session a finished ceremony opens (`docs/sessions.md`), the
bearer token that reaches a narrower set of routes (`docs/api.md`), or the Host pinning that bounds which
hostname a credential may be bound to (`docs/tls.md`).

**A pairing code authorizes one registration and authenticates nothing.** No session, no cookie and no
CSRF token exists on the strength of a code. The code buys the ceremony's options, and the session is opened
only as the answer to a credential response the device's own authenticator signed. It works once and dies
with its TTL, which is what makes it safe to print on a terminal.

## What must stay true

- **A session opens only as the answer to a finished ceremony.** Both ceremony finishes end at
  `openSession`, and nothing else on these routes writes the session cookie. A route that opens a session
  on any other condition has turned the pairing code into a password.
- **One code writes one passkey, and it is spent when that passkey row is written.** A ceremony that never
  finished has spent nothing, so the same code pairs on the next try. Two tickets may be open at once and
  the second to reach the write is refused: the spend is one delete of the code's entry. A write that failed
  puts its code back, and a revoke in between outranks that — a revoked code stays revoked.
- **A code never becomes a log record, a query string, or a child process's environment.** The boot banner
  writes it once, with no record prefix on any row. A browser gets it through the URL fragment, and it lives
  in memory. A secret with no on-disk copy cannot leak from a backup.
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
from a look-alike-free alphabet and keeps only `hex(sha256(code))`. `Claim` checks a presented code and
returns the key its entry is spent by. The registration it pays for then calls `spend`, which deletes that
entry, or `undo`, which returns it when the row could not be written. A `RevokeAll` in between outranks the
undo, which is why a spend carries the revoke count with it. Unknown, expired and spent are one error —
`ErrInvalidPairingCode` — because telling them apart tells a guesser how close they came.
`NormalizePairingCode` is the one shape a typed code takes, mirrored by `normalizePairingCode`
(`frontend/src/lib/utils/login-flow.ts`) so the form's own button agrees with what the server accepts.
`FormatPairingCode`'s two groups are mirrored by `pairingCodeInput`, which groups the field as it is typed:
what a person sees is the shape they are reading off the terminal.
TTLs: `BootPairingTTL` for the printed code, `DevicePairingTTL` for one minted from Settings, at most
`pairingMaxLive` live at once. There is no per-code attempt counter on purpose: a wrong code has no entry
to count against, so the login rate limiter on the route (`MIKE_LOGIN_RATE_LIMIT`) is the bound.

**Tickets** (`backend/auth/ticket.go`). `Tickets.Seal`/`Open` carry the library's `SessionData`, and the
caller's own bytes beside it, across the two requests: HMAC-SHA256 over purpose + payload, keyed by HKDF over
`MIKE_ENCRYPTION_KEY`, bounded by `TicketTTL` and `ticketMaxPayloadBytes`. A registration seals the claimed
code's key there, which is how its finish knows what to spend. A table would be state held for a client that closed the tab —
an abandoned ceremony is the normal outcome — and the purpose inside the MAC, rather than inside the
payload, is what stops one ceremony's ticket being presented to the other.

**Presentation** (`backend/auth/pairing_banner.go`, `backend/auth/pairing_qr.go`,
`backend/server/boot_pairing.go`). `PairingURL` puts the code in the fragment. The boot printout is
`PairingBanner`: the code on a row of its own, padded inside a frame, with the link and the expiry beside it.
It is padding and rule characters rather than a picture, because a terminal's cell is not a known shape. A
block-drawn QR is square only while the reader's line height happens to be twice the font's advance width, and
no terminal may be assumed to be that. The row the code sits on is a contract across languages: the browser
lane mirrors it to pair its test device from the server's log (`PAIRING_CODE_ROW` in
`frontend/tests/helpers-auth.mjs`), and `TestPairingBannerShowsTheCodeOnItsOwnRow` holds the shape. A
scannable picture exists in one place only: `PairingQRPNG`, the PNG the Settings mint returns, where the pixel
grid is ours. `pairAtBoot` mints and prints only while `PasskeyCount()` is zero, so an unattended restart never
announces a live code into a log nobody is reading, and warns when the listen address is not loopback or the
printed host cannot hold a passkey. `logging.Banner` writes the box to the same sink as the log lines, with no
prefix on any row — `TestBootPairingPrintsTheCodeRowWithNoPrefix` holds that.

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
`navigator.credentials` insists on, and `passkeySupport()` names why a ceremony cannot run here.
`insecure-context` is the one the person can act on, and on that one the gate refuses a prompt it knows the
browser will not run and says the connection is not secure. `GET /api/auth/session` answers
`tls_ca_available`, and where it says yes the notice also links the local CA and the steps to install it —
that device has no session to download the certificate with. See `docs/tls.md → Certificates on disk`.
`$lib/utils/login-flow.ts` owns every branch:
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
rpID scoping, and the code spent by its own registration — including one code paid against two concurrent
registrations, and a revoke outranking a claim still in flight. The ticket's purpose, expiry and sealed
claim, the last-passkey refusal, the atomic reset and every route refusal above are in the same run. Run it under `make check-race` for
the concurrent case. `make test-e2e SPECS=login` walks a
browser through pairing, registration and assertion; `make test-e2e SPECS=settings-passkeys` drives the
Settings surface, including the minted code a second device pairs with.
