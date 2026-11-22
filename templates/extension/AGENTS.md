# Writing {{NAME}}

This repository is a MIKE extension. It is not a web app, a package or a build: it is a folder of
files MIKE reads, and the only build step is the person pressing **Try Extension**.

## What MIKE does with this folder

- Only the files `manifest.json` names are used, plus the entry files your own `index.html` loads.
  An unmentioned file is dead weight that still gets served.
- The folder is served to the browser at `/extensions/_dev/<this-repo>/`, **without a session and
  without a login**. Anything secret stays out of it: a `.git/config` remote URL, a token, a private
  path. Dot-prefixed names are refused by the server, so a `.env` is unreachable rather than safe.
- An edit to an already-named file reaches the browser on the next request. An edit to
  `manifest.json` — a new capability, a new file name — lands only when the person presses **Try
  Extension**, which validates the manifest and answers with the failure text. Relay that text to
  them; it names the field that is wrong.
- Nothing hot-reloads the manifest: the press loads it and the tab reloads.

## How to work

1. Read the Plan skill and agree on a plan before implementing anything.
2. Read `.agents/skills/extension-authoring/SKILL.md`. It decides the type from the description the
   person gave, and the type decides everything else — whether there is a Dockerfile, whether a
   container runs, whether the extension may reach a network at all.
3. Write `manifest.json` at this root, beside the files it names. Names inside the extension are
   relative to this directory and never climb out of it.
4. Keep the person's own words out of committed files. The tree is public; their request belongs in
   the task, not in a comment.
5. Say which type you chose and why, in the task's final message, in the person's terms.

## Tests

`tests/` holds the shape a published extension's tests take: a plain Node script driving a browser
against a running MIKE. Running it needs node and a browser in the container, which the default
image has neither of — so tests here are opt-in, and the project needs its own Dockerfile (a build
always has a network) or an image chosen in Sandbox Settings. Never leave a test that the default
image cannot run as the only evidence something works; say what you ran and what you did not.
