# Writing {{NAME}}

This repository is a MIKE extension. It is not a web app, a package or a build: it is a folder of
files MIKE reads, and the only build step is the person pressing **Try Extension**.

## What MIKE does with this folder

- The version the person sees is the copy **Try Extension** takes of this branch's worktree, served
  at `/extensions/_dev/_try/<this-repo>/<task-id>/` — **without a session and without a login**.
  Anything secret stays out of it: a `.git/config` remote URL, a token, a private path.
  Dot-prefixed names are refused by the server, so a `.env` is unreachable rather than safe.
- A press copies the repository's own files, plus what `manifest.json` names and the entry files your
  own `index.html` loads. An unmentioned sibling file is dead weight that still gets served.
- An edit reaches the browser only on the next press, which re-copies the worktree; a refresh shows
  the bytes from the last one. The press is also what validates the manifest and answers with the
  failure text — relay it, it names the field that is wrong. A version asking for something it has
  not been granted comes back as a question before it runs.

## How to work

1. Read the `plan-first` skill and agree on a plan before implementing anything.
2. Read `.agents/skills/extension-authoring/SKILL.md` and follow its loop. It decides the type from
   the description the person gave, and the type decides everything else — whether there is a
   Dockerfile, whether a container runs, whether the extension may reach a network at all.
3. Keep the person's own words out of committed files. The tree is public; their request belongs in
   the task, not in a comment.

## Tests

`tests/` holds the shape a published extension's tests take: a plain Node script driving a browser
against a running MIKE. Running it needs node and a browser in the container, which the default
image has neither of — so tests here are opt-in, and the project needs its own Dockerfile (a build
always has a network) or an image chosen in Sandbox Settings. Never leave a test that the default
image cannot run as the only evidence something works; say what you ran and what you did not.
