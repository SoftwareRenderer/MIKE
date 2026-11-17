# Tests

A published MIKE extension tests itself with a plain Node script that drives a browser against a
running MIKE — no test framework, no fixture service. The shape is one file, run by hand:

```
node tests/<name>.mjs
```

against the dev server (`MIKE_BASE`, default `https://localhost:3000`), signing in with a token the
operator provides rather than driving the passkey ceremony.

## Whether you can run it here

Probably not, and that is worth saying out loud rather than writing a test nobody executed. The
default image for a project with no Dockerfile is `docker.io/library/ubuntu:latest`, which ships
**neither node nor a browser**. Running a test like this needs one of:

- a `Dockerfile` at this root installing node and the browser (a build always has a network), or
- an image chosen for the project under Sandbox Settings.

Either is the person's decision, not yours: it changes what their tasks run on. Ask, or write the
test and say plainly that it has not been run and what it would take to run it. For a `ui`, `css` or
`i18n` extension the honest evidence is the person pressing **Try Extension** and refreshing — the
manifest was validated when it loaded, and the rest is what they see.
