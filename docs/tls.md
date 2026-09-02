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
Import `data/tls/mike-ca.crt` into a device's trust store to make the certificate valid there —
distribution is file-only (never served over HTTP; the CA *key* never leaves the data dir, keys
are `0600`).

The CA is reused across restarts and never rotated automatically; the leaf is re-issued only
when the host's names change (network or hostname change). There is **no expiry-based renewal** —
refresh an expired leaf by deleting `mike.crt`/`mike.key` and restarting (keep the CA). The server
sends the leaf alone: the CA is a root, so an importing client needs no intermediate.

Plain HTTP on a TLS-enabled server gets a 301 → https. Custom certs: `--tls-cert/--tls-key` or
`MIKE_TLS_CERT`/`MIKE_TLS_KEY` (no CA involved). Disable: `--no-tls` or `MIKE_TLS=0`. With TLS
active, WebSockets upgrade to `wss://` and session cookies get `Secure` automatically.

## A certificate is not the gate

Reaching the server at all is governed by `MIKE_LISTEN` (loopback by default) and by Host pinning
— see `MIKE_ALLOWED_HOSTS`. A SAN covering the LAN does *not* make the LAN usable: a request whose
`Host` names that hostname/IP is rejected with 421 until it is also listed there (or the server is
explicitly listening on that address).

A reverse proxy that forwards the public domain (`proxy_set_header Host $host`) hits exactly this —
nginx's default (`$proxy_host`, i.e. the upstream address) passes unlisted. Behind a proxy also set
`MIKE_TRUST_PROXY=1`, or `X-Forwarded-Proto` (HTTPS redirect) and `X-Forwarded-For` (rate-limit IP)
are ignored.

Entries are matched literally — e.g. `MIKE_ALLOWED_HOSTS=kanban.example.com,192.168.1.20`. Ports are
stripped on both sides, so an `example.com` entry answers `example.com:443`; a missing/empty `Host` is
rejected too.

## Weak master password + exposed listen address is fatal

A `MIKE_LOGIN_PASSWORD` shorter than 8 characters, or one of the known defaults (`test`, `dev`,
`admin`, `password`, `pass`, `changeme`), refuses to start on any non-loopback listen address.
Loopback + weak stays allowed so `make run` (password `test`) keeps working.

## Tests

`backend/tls/tls_test.go` (CA is a real CA, leaf verifies against it, SAN set + re-issue rules,
CA/leaf reuse), `backend/server/listener_test.go`.
