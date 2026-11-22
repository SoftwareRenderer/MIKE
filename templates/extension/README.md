# {{NAME}}

An extension for [MIKE](https://getmike.dev), written by an agent on this machine.

MIKE runs the folder as soon as a version of it is loaded, so what you see in the browser is the
bytes in this directory — nothing is built, bundled or installed on the way. `manifest.json` at this
root says what the extension is; press **Try Extension** on a task in Review to load that task's
version, and the tab reloads itself to show it.

Start by reading `AGENTS.md`, then the `extension-authoring` skill under `.agents/skills/`. The
skill decides the extension's type from what it is for, and `references/manifest.md` is the whole
manifest grammar.
