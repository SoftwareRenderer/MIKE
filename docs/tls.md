# TLS / Certificate Model

Read this before touching `backend/tls/`, the listen/Host-header logic, or anything that
decides whether a peer may reach the server. The invariant list that must survive any edit
lives in `AGENTS.md → Server Management`.

## Certificates on disk

TLS is **not optional**: the server always serves it, signed by a local CA created on first start in
`data/tls/`: `mike-ca.crt`/`mike-ca.key` (self-signed root, `O=MIKE, CN=MIKE Local CA`, 10 years) signs the
server certificate `mike.crt`/`mike.key` (`O=MIKE, CN=localhost`, 1 year). `--tls-cert`/`--tls-key` point
the server at a certificate of the operator's instead — a way to change which certificate is served, never
a way out of TLS — and they have no environment spelling.

The leaf's SANs cover `localhost`/127.0.0.1/::1 **plus the machine hostname, `<hostname>.local`
and every non-loopback address of the host**, so other devices can reach it by name or LAN IP.
Import `data/tls/mike-ca.crt` into a device's trust store to make the certificate valid there.
While that default certificate is in use the CA is also downloadable: `GET /api/tls/ca` serves it,
Settings → About → System Info links to it, and so does the login gate when the browser reports an
insecure connection (`docs/passkeys.md → How it is held`). `tls_ca_available` is what makes either link appear,
in `GET /api/config` and in the public `GET /api/auth/session`. Both links and the download itself come from
the one `localCAvailable()` decision made when the server is built, so a link can never point at a 404.

The route is **public for GET**, listed in `publicPaths` beside the ceremony routes and the session check. The
device that needs the certificate is the one with no session yet: a browser that will not run a passkey prompt
is being told to trust this address before it can sign in. That costs nothing secret. A root certificate is
public data by design; the CA *key* is never served and never leaves the data dir (keys are `0600`). The only
file the route can name is `Config.CAFile` — no request field is ever a path — and Host pinning still applies.
`tls.CertPEM` answers with a certificate block or an error, so the download cannot be turned into a key dump
even if that path were swapped for a key file. A `--tls-cert`/`--tls-key` install has no CA of
ours, so the route answers 404 and both links are absent rather than explained.

The CA is reused across restarts and never rotated automatically; the leaf is re-issued only
when the host's names change (network or hostname change). There is **no expiry-based renewal** —
refresh an expired leaf by deleting `mike.crt`/`mike.key` and restarting (keep the CA). The server
sends the leaf alone: the CA is a root, so an importing client needs no intermediate.

A plain-HTTP connection reaching the TLS port is redirected, not served: the listener's first-byte sniff
hands it to the plain half, which answers 301 → https (`docs/lifecycle.md → The TLS/plain listener`).
WebSockets upgrade to `wss://`.

The session cookie's `Secure` flag follows the request, not the boot configuration
(`requestIsHTTPS`): TLS terminated here, or a TLS-terminating proxy whose `X-Forwarded-Proto: https`
is honoured because `MIKE_TRUST_PROXY=1` is set. One reading, two users — the redirect above and
the cookie — because a deployment where they disagree is the failure the flag exists to prevent.
A proxy must therefore set `proxy_set_header X-Forwarded-Proto $scheme;` *and* have
`MIKE_TRUST_PROXY=1`; with either missing, MIKE can only see cleartext on that hop and marks the cookie
honestly, which is also the reading that tells the browser the connection is not a secure one. There is
deliberately **no variable** for the flag.

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
`https://…` or `http://localhost`. Plain HTTP on a LAN address therefore has **no login at all**. The gate
says the connection cannot carry a passkey, hands out the local CA where there is one to hand out, and
explains how to install it — instead of offering a button that can only fail. Serve TLS (here or
at the proxy, with `MIKE_TRUST_PROXY=1` so `X-Forwarded-Proto` decides the cookie's `Secure` flag
and the origin a signature is checked against) or reach the install at `localhost`.

The name is the credential's audience, not a cosmetic detail: an address is not a relying-party id, so
`https://192.168.1.20:3000` has no passkey to offer even over TLS. Pin the hostname in
`MIKE_ALLOWED_HOSTS` and pair through that name. A fresh install prints its pairing code and says so when
the address it printed cannot hold the credential it is inviting — `docs/passkeys.md → How it is held`.

## Tests

`backend/tls/tls_test.go` holds the CA and its leaf: a real CA, the leaf verifying against it, the SAN set
and re-issue rules, CA/leaf reuse, and `CertPEM` answering with a certificate and nothing else.
`backend/server/handler_tls_test.go` holds the download: it answers with no session, 404s without a local
CA, takes only GET, still pins the `Host`, and reports `tls_ca_available` in `/api/config` and
`/api/auth/session`.
The listener's test is listed in `docs/lifecycle.md`.

## Checking this

`go test ./backend/tls/ ./backend/server/` covers the CA and its leaf, the download's guards, the Host
pinning that decides reachability, and the per-request `Secure` flag a forwarded `X-Forwarded-Proto` sets.
