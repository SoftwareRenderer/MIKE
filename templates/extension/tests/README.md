# Tests

A published MIKE extension tests itself with a plain Node script that drives a browser against a
running MIKE — no test framework, no fixture service. The shape is one file, run by hand:

```
node tests/<name>.mjs
```

against the dev server (`MIKE_BASE`, default `https://localhost:3000`).

## What a script can authenticate as

There is no password anywhere in MIKE, and a session exists only as the answer to a finished passkey
ceremony, which a hand-rolled script does not complete. A bearer token the operator provisions is the
other credential, and it reaches **the project and task routes only** — enough to create the task or
the data your extension reads, not enough to open the board, its WebSocket, or
`/api/extensions/**`. So a script that must *see* the extension in the interface either reuses a
browser session the operator already holds, or it is not a script anyone can run unattended. Write
the assertion you can actually make, and say which of the two it is.

## Whether you can run it here

Probably not, and that is worth saying out loud rather than writing a test nobody executed. The
default image for a project with no Dockerfile is `docker.io/library/ubuntu:latest`, which ships
**neither node nor a browser**. Running a test like this needs one of:

- a `Dockerfile` at this root installing node and the browser (a build always has a network), or
- an image chosen for the project under Sandbox Settings.

Either is the person's decision, not yours: it changes what their tasks run on. Ask, or write the
test and say plainly that it has not been run and what it would take to run it. For a `ui`, `css` or
`i18n` extension the honest evidence is the person pressing **Try Extension** — the manifest was
validated when it loaded, and the rest is what they see.
