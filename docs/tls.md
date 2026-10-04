# TLS / Certificate Model

Read this before touching `backend/tls/`, the listen/Host-header logic, or anything that
decides whether a peer may reach the server. The invariant list that must survive any edit
lives in `AGENTS.md → Server Management`.

## Certificates on disk

TLS is **enabled by default**, signed by a local CA created on first start in `data/tls/`:
`mike-ca.crt`/`mike-ca.key` (self-signed root, `O=MIKE, CN=MIKE Local CA`, 10 years) signs the
server certificate `mike.crt`/`mike.key` (`O=MIKE, CN=localhost`, 1 year).

The leaf's SANs cover `localhost`/127.0.0.1/::1 **plus the machine hostname, `<hostname>.local`
and every non-loopback address of the host**, so other devices can reach it by name or LAN IP.
Import `data/tls/mike-ca.crt` into a device's trust store to make the certificate valid there.
While that default certificate is in use the CA is also downloadable: `GET /api/tls/ca` serves it and
Settings → About → System Info links to it (`tls_ca_available` in `GET /api/config` is what makes the
link appear, and it is derived from the same `tls.Config` the route checks, so a link can never point
at a 404). The route is session-authed like every other `/api` path, pinned to GET, and the only file
it can name is `Config.CAFile` — no request field is ever a path. And `tls.CertPEM` answers with a
certificate block or an error, so the download cannot be turned into a key dump even if that path
were swapped for a key file. The CA *key* is never served and never leaves the data dir (keys are
`0600`). `--no-tls` and a `--tls-cert`/`--tls-key` pair offer no download: neither has a CA of ours,
so the route answers 404 and the System Info row is absent rather than explained.

The CA is reused across restarts and never rotated automatically; the leaf is re-issued only
when the host's names change (network or hostname change). There is **no expiry-based renewal** —
refresh an expired leaf by deleting `mike.crt`/`mike.key` and restarting (keep the CA). The server
sends the leaf alone: the CA is a root, so an importing client needs no intermediate.

Plain HTTP on a TLS-enabled server gets a 301 → https. Custom certs: `--tls-cert`/`--tls-key`, which have
no environment spelling and bring no CA of ours. Disable: `--no-tls` or `MIKE_TLS=0`. With TLS active,
WebSockets upgrade to `wss://`.

The session cookie's `Secure` flag follows the request, not the boot configuration
(`requestIsHTTPS`): TLS terminated here, or a TLS-terminating proxy whose `X-Forwarded-Proto: https`
is honoured because `MIKE_TRUST_PROXY=1` is set. One reading, two users — the redirect above and
the cookie — because a deployment where they disagree is the failure the flag exists to prevent.
A proxy must therefore set `proxy_set_header X-Forwarded-Proto $scheme;` *and* have
`MIKE_TRUST_PROXY=1`; with either missing, MIKE can only see cleartext and marks the cookie
honestly. The single case that stays unencrypted is `--no-tls` with no proxy — loopback
development and the e2e harness, where a `Secure` cookie could never be stored and no one could
sign in. There is deliberately **no variable** for the flag.

## A certificate is not the gate

Reaching the server at all is governed by `MIKE_LISTEN` (loopback by default) and by Host pinning
— see `MIKE_ALLOWED_HOSTS`. A SAN covering the LAN does *not* make the LAN usable: a request whose
`Host` names that hostname/IP is rejected with 421 until it is also listed there (or the server is
explicitly listening on that address).

A reverse proxy that forwards the public domain (`proxy_set_header Host $host`) hits exactly this —
nginx's default (`$proxy_host`, i.e. the upstream address) passes unlisted. Behind a proxy also set
`MIKE_TRUST_PROXY=1`, or `X-Forwarded-Proto` (HTTPS redirect, and the session cookie's `Secure`
flag) and `X-Forwarded-For` (rate-limit IP) are ignored.

Entries are matched literally — e.g. `MIKE_ALLOWED_HOSTS=kanban.example.com,192.168.1.20`. Ports are
stripped on both sides, so an `example.com` entry answers `example.com:443`; a missing/empty `Host` is
rejected too.

Which bytes reach this decision is the listener's own rule — the TLS/plain first-byte sniff, the queue it
feeds, and what may end `Serve`: `docs/lifecycle.md → The TLS/plain listener`.

## Login needs a secure context, and a hostname

A passkey is minted for a hostname and only ever released over a connection the browser calls secure:
`https://…` or `http://localhost`. Plain HTTP on a LAN address therefore has **no login at all** — the
gate says the connection cannot carry a passkey instead of offering a button that can only fail. Serve TLS
(here or at the proxy, with `MIKE_TRUST_PROXY=1` so `X-Forwarded-Proto` decides the cookie's `Secure` flag
and the origin a signature is checked against) or reach the install at `localhost`.

The name is the credential's audience, not a cosmetic detail: an address is not a relying-party id, so
`https://192.168.1.20:3000` has no passkey to offer even over TLS. Pin the hostname in
`MIKE_ALLOWED_HOSTS` and pair through that name. A fresh install prints its pairing code and says so when
the address it printed cannot hold the credential it is inviting — `docs/passkeys.md → How it is held`.

## Tests

`backend/tls/tls_test.go` (CA is a real CA, leaf verifies against it, SAN set + re-issue rules,
CA/leaf reuse, `CertPEM` serves a certificate and nothing else), `backend/server/handler_tls_test.go`
(the download: needs a session, 404 without a local CA, GET-only, and `tls_ca_available` tracks it).
The listener's test is listed in `docs/lifecycle.md`.

## Checking this

`go test ./backend/tls/ ./backend/server/` covers the CA and its leaf, the download's guards, the Host
pinning that decides reachability, and the per-request `Secure` flag a forwarded `X-Forwarded-Proto` sets.
