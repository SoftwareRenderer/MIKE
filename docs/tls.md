# TLS / Certificate Model

Read this before touching `backend/tls/`, the listen/Host-header logic, or anything that
decides whether a peer may reach the server. The invariant list that must survive any edit
lives in `AGENTS.md → Server Management`.

## Certificates on disk

TLS is **not optional**. The server always serves it, signed by a local CA created at first start in
`data/tls/`: the root (`mike-ca.crt`/`mike-ca.key`, 10 years) signs the server certificate
(`mike.crt`/`mike.key`, 1 year). `--tls-cert`/`--tls-key` point the server at an operator's
certificate instead — a way to change which certificate is served, but never a way out of TLS.

The leaf's SANs cover `localhost`/127.0.0.1/::1 **plus the machine hostname, `<hostname>.local`
and every non-loopback address of the host**, so other devices can reach it by name or LAN IP.
Import `data/tls/mike-ca.crt` into a device's trust store to make the certificate valid there.
While that default certificate is in use, the CA is downloadable: `GET /api/tls/ca` serves it,
Settings → About → System Info links to it, and so does the login gate when the browser reports an
insecure connection (`docs/passkeys.md → Presentation & UI`). `tls_ca_available` controls these links
in `GET /api/config` and `GET /api/auth/session`. Both links and the download depend on a single
`localCAvailable()` decision made at boot.

The route is **public for GET**, listed in `publicPaths` beside the ceremony routes and the session check.
The device that needs the certificate is the one with no session yet: a browser that cannot run a
passkey prompt is told to trust this address before it can sign in. A root certificate is public data;
the CA *key* never leaves the data dir. Keys are written with owner-only permissions. If the target
is not a regular file, the write is refused. The only file the route can name is `Config.CAFile` — no
request field is ever a path — and Host pinning still applies. `tls.CertPEM` answers with a certificate
block or an error, so the download cannot be turned into a key dump. An operator's `--tls-cert`/`--tls-key`
install has no CA of ours, so the route answers 404 and links are absent.

The leaf is re-issued when the host's names change (network or hostname change), or whenever it expires
inside `leafRenewalWindow` (30 days). A server restarted at least that often never serves an expired
certificate. Replacing a leaf costs nothing: no device remembers it, and the CA is untouched.

The CA is reused if it is valid on load: it must parse, the key must match the certificate, it must have
`KeyUsageCertSign`, and it must not be expired. If any check fails, the CA is replaced. Replacement is an
`Error` naming both files and the reason. Every device that imported the old root stops trusting the
server at that moment; the log line is the only record. A first install has no root and says nothing.

The root signs leaves only: `pathlen:0` with `KeyUsageCertSign|CRLSign`. A key leaked from the data
directory cannot mint a CA that chains (`TestLocalCARejectsAChain`). A healthy root is never rotated.

The pair to be served is loaded before anything is announced: `LoadWithCert` loads an operator's
`--tls-cert`/`--tls-key` (naming the flags if they do not form a pair), and `Serve` loads the leaf
this package wrote. A pair that cannot be served stops the boot. Otherwise, the listener holds a nil
config: every ClientHello panics inside `crypto/tls`, net/http recovers it per connection, and the port
answers while serving nothing.

The server sends the leaf alone: the CA is a root, so an importing client needs no intermediate.

A plain-HTTP connection reaching the TLS port is redirected, not served: the listener's first-byte sniff
hands it to the plain half, which answers 301 → https (`docs/lifecycle.md → TLS and Plain Listener`).
WebSockets upgrade to `wss://`.

The session cookie's `Secure` flag follows the request, not the boot configuration
(`requestIsHTTPS`): TLS terminated here, or a TLS-terminating proxy whose `X-Forwarded-Proto: https`
is honoured because `MIKE_TRUST_PROXY=1` is set. Proxies must set `proxy_set_header X-Forwarded-Proto $scheme;`
and `MIKE_TRUST_PROXY=1`. If either is missing, MIKE sees cleartext and marks the cookie as insecure,
which also tells the browser the connection is not secure. There is deliberately **no variable** for the flag.

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
feeds, and what may end `Serve`: `docs/lifecycle.md → TLS and Plain Listener`.

## Login needs a secure context, and a hostname

A passkey is minted for a hostname and only ever released over a connection the browser calls secure:
`https://…` or `http://localhost`. Plain HTTP on a LAN address has **no login at all**. The gate
explains that the connection cannot carry a passkey, hands out the local CA if available, and
explains how to install it. The pairing body offers the CA regardless of the connection: the secure-context
rule belongs to the browser being paired. To satisfy it, use TLS (here or at a proxy with
`MIKE_TRUST_PROXY=1`) or reach the install at `localhost`.

The name is the credential's audience, not a cosmetic detail: an address is not a relying-party id, so
`https://192.168.1.20:3000` has no passkey to offer even over TLS. Pin the hostname in
`MIKE_ALLOWED_HOSTS` and pair through that name. A fresh install prints its pairing code, and warns about
every address it printed that cannot hold the credential it is inviting —
`docs/passkeys.md → Presentation & UI`.

## Tests

`backend/tls/tls_test.go` holds the CA and its leaf: a real CA, the leaf verifying against it, the SAN set
and re-issue rules, CA/leaf reuse, and `CertPEM` answering with a certificate and nothing else. The root's
`pathlen:0` is held by `TestLocalCARejectsAChain`, which also proves one link still chains. An expiring
leaf is replaced from an untouched CA, and a replaced root is announced at `Error`
(`TestEnsureCA_AnnouncesAReplacedRoot`, whose `checkReplacement` also holds that the replacement is a CA
and signs the served leaf). Keys are owner-only both created and replacing
(`TestKeyFilesAreOwnerOnly`), and an unloadable pair is refused by `--tls-cert`/`--tls-key` and by `Serve`
before anything is served.
`backend/server/handler_tls_test.go` holds the download: it answers with no session, 404s without a local
CA, takes only GET, still pins the `Host`, and reports `tls_ca_available` in `/api/config` and
`/api/auth/session`. `backend/server/server_tls_test.go` holds what `Serve` does with a pair it cannot
load: it returns the error instead of binding a port or printing a pairing code.
The listener's test is listed in `docs/lifecycle.md`.

## Checking this

`go test ./backend/tls/ ./backend/server/` covers the CA and its leaf, the download's guards, the Host
pinning that decides reachability, and the per-request `Secure` flag a forwarded `X-Forwarded-Proto` sets.
`backend/tls` is not in `RACE_PKGS`, so `go test -race ./backend/tls/` is its own command.
