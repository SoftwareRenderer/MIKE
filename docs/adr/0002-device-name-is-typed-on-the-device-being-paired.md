# ADR-0002 — A device names itself where it is being paired

**Status:** accepted

**Rules that follow:** `docs/passkeys.md → What must stay true`, `docs/passkeys.md → How it is held`

## Abstract

The name a passkey row carries is typed on the device being paired, into a field the pairing form
prefills. The guess it prefills comes from the server — `GET /api/auth/session` answers `device_name`, read
off that request's own User-Agent by the same function that names an unnamed row — and a name typed while
minting a code travels inside the pairing link's fragment, not in the server's memory of the code.

## Context

`POST /api/auth/passkey/register/finish` already took a `name`, and `$lib/api/auth.ts` already sent one;
nothing ever passed it, so every device was called what its User-Agent looked like and the only way to fix
the label was Settings → rename afterwards. Two questions had no obvious answer: who reads the browser to
guess a name, and where a name given by the *minting* device should live.

## Decision

The server reads the browser. `guessedDeviceLabel` (`backend/server/handler_passkey.go`) is the one reader
of a User-Agent, and it can say it read nothing; `handleAuthSession` answers that reading, and
`LoginModal.svelte` opens its field with it. The browser never re-reads `navigator.userAgent`.

The minting device's name rides the link. `PairingURL(origin, code, label)` appends `&name=…` to the
fragment the code already rides in, `handlePairingMint` validates the label before it mints, and
`PairingCodes` still holds only a code and an expiry. Whatever the pairing field holds when the person
submits is what the row is written with; blank is the fallback.

## Consequences

What the person was shown and what was stored are decided in one place, so they cannot disagree, and a name
nobody used costs no state: nothing expires, nothing is undone, and a code read off a terminal is still
eight characters. It costs a second prefill source — the link outranks the browser guess — and it means a
code typed by hand, rather than opened, arrives unnamed. The link's own label is untrusted text: capped by
`pairingLinkFromHash` on the way in and put through `cleanPasskeyName` on the way back.

Two alternatives were rejected. Reading `navigator.userAgent` in TypeScript would have duplicated
`deviceLabel`'s grammar in a second language, and the AGENTS comment rule ("one home") is the whole reason
that grammar has product tokens in it at all: a WebKit browser ends with `Safari/<engine version>` whatever
it is, and the reader has to know that. Storing the mint-time label against the pairing code would have
reached the form only at `register/begin` — after the person had already seen and edited the field — or
needed a public route that answers a lookup keyed by a code, which is a second door onto a credential that
is live until spent.

What would reopen it: a pairing flow where the registering device cannot see the link at all (a code
relayed by voice, say) would want the mint's label to outrank the guess without travelling in the link, and
that needs the code to remember it.
