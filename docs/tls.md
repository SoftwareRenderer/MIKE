# TLS / Certificate Model

Read this before touching `backend/tls/`, the listen/Host-header logic, or anything that
decides whether a peer may reach the server. The invariant list that must survive any edit
lives in `AGENTS.md → HTTPS / TLS`.

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
it can name is `Config.CAFile` — no request field is ever a path — and `tls.CertPEM` answers with a
certificate block or an error, so the download cannot be turned into a key dump even if that path
were swapped for a key file. The CA *key* is never served and never leaves the data dir (keys are
`0600`). `--no-tls` and a `--tls-cert`/`--tls-key` pair offer no download: neither has a CA of ours,
so the route answers 404 and the System Info row is absent rather than explained.

The CA is reused across restarts and never rotated automatically; the leaf is re-issued only
when the host's names change (network or hostname change). There is **no expiry-based renewal** —
refresh an expired leaf by deleting `mike.crt`/`mike.key` and restarting (keep the CA). The server
sends the leaf alone: the CA is a root, so an importing client needs no intermediate.

Plain HTTP on a TLS-enabled server gets a 301 → https. Custom certs: `--tls-cert/--tls-key` or
`MIKE_TLS_CERT`/`MIKE_TLS_KEY` (no CA involved). Disable: `--no-tls` or `MIKE_TLS=0`. With TLS
active, WebSockets upgrade to `wss://`.

The session cookie's `Secure` flag follows the request, not the boot configuration
(`requestIsHTTPS`): TLS terminated here, or a TLS-terminating proxy whose `X-Forwarded-Proto: https`
is honoured because `MIKE_TRUST_PROXY=1` is set. One reading, two users — the redirect above and
the cookie — because a deployment where they disagree is the failure the flag exists to prevent.
A proxy must therefore set `proxy_set_header X-Forwarded-Proto $scheme;` *and* have
`MIKE_TRUST_PROXY=1`; with either missing, MIKE can only see cleartext and marks the cookie
honestly. The single case that stays unencrypted is `--no-tls` with no proxy — loopback
development and the e2e harness, where a `Secure` cookie could never be stored and no one could
sign in. (`MIKE_SECURE_COOKIES` used to force the flag from the process side; it was removed
because the proxy case is detectable from the request, and an opt-in flag is off in exactly the
deployment it was written for.)

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

## Weak master password + exposed listen address is fatal

A `MIKE_LOGIN_PASSWORD` shorter than 8 characters, or one of the known defaults (`test`, `dev`,
`admin`, `password`, `pass`, `changeme`), refuses to start on any non-loopback listen address.
Loopback + weak stays allowed so `make run` (password `test`) keeps working.

## Tests

`backend/tls/tls_test.go` (CA is a real CA, leaf verifies against it, SAN set + re-issue rules,
CA/leaf reuse, `CertPEM` serves a certificate and nothing else), `backend/server/handler_tls_test.go`
(the download: needs a session, 404 without a local CA, GET-only, and `tls_ca_available` tracks it),
`backend/server/listener_test.go`.

## The listener (moved from AGENTS.md)
- **The TLS-vs-plain first-byte sniff must not run inside `hybridListener.Accept()`** (`backend/server/server.go`): `net/http` accepts **serially**, so one peer that connects and sends nothing stalls every later handshake, and returning its EOF/reset reads as a dead listener, ends `Serve` and kills the process. `Accept()` waits on a queue that a per-connection sniff goroutine fills — no worker pool, since N workers still block everyone at N silent peers. Sniffed-out peers are closed and logged per host, never handed to `net/http`; only the listener's own accept error may end `Serve`. Tests: `backend/server/listener_test.go`.
