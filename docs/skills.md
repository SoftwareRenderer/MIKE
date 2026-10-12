# Skills: what a task is told it can read

`backend/prompts` owns this. A skill is a directory holding a `SKILL.md` whose YAML frontmatter carries
both `name` and `description`; the system prompt lists each one by name, description and location, and
the agent opens the file when a task matches. This file covers which directories are scanned, what the
agent is told to open, and how a same-named pair stays addressable. It does not cover the prompt
templates themselves, or extensions (`docs/extension-lifecycle.md`), which are a different unit of
installation.

## What must stay true

- **Two roots, one list.** A task's list is the bundled `prompts/skills/` plus the project's own
  `.agents/skills/`, the latter read from **the project directory** — not the task's worktree, which is a
  separate tree under the workspace directory and does not contain it. Consequence: a task branch that
  edits a `SKILL.md` in its worktree does not change what is advertised.
- **Scanned one level deep.** `<root>/<skill>/SKILL.md` is a skill; anything deeper is not searched.
- **Nothing is dropped for a name clash.** Both documents are listed, and the later arrival's display
  name gains a tag (see the suffix rule below). Silence is a worse outcome than an awkward name.
- **The location is a path the agent can open.** A host task is given the host path. A sandboxed task
  cannot read a host path, so each root is advertised at the container mount it is bound to:
  `sandbox.SkillsMount` for the bundled packages, `sandbox.ProjectSkillsMount` for the project's.
- **The scan is a read of untrusted bytes.** A `SKILL.md` is read capped, as a regular file, and only
  when the name still resolves inside the root it was found in. A link or an oversized file is skipped,
  not followed.
- **A skill is text the agent chooses to read.** Nothing here grants a tool, a mount or a network path.

## How it is held

- `prompts.ProjectSkillsSubdir` and `prompts.ProjectSkillsDir(projectPath)` name the project's directory
  once, at both ends: `exec.skillRoots` builds the prompt's roots from it, and `sandbox.ProjectOpts`
  binds that same value read-only (`docs/sandbox-runtime.md → What a task container binds besides its
  worktree`). A prompt that names a directory nobody mounted hands the agent a file that will not open.
- `prompts.SkillRoot{Dir, Prefix, Origin}` is one scanned directory plus the path the agent reads it
  through. `exec.skillRoots` names each root's mount and blanks `Prefix` for a task running on the host,
  which reads the host directory.
- `prompts.DiscoverSkills(roots...)` scans in the order given and keeps every package.
  `uniqueSkillName` gives each a display name no other carries: the plain name first, then
  `name (origin)`, then `name (directory)`, then a counter. A tag equal to the name is skipped — it says
  nothing the name did not.
- The read is `fsutil.Contained` then `fsutil.OpenChecked` then `io.LimitReader` at
  `prompts.skillReadLimit`. A symlinked package *directory* never reaches that code:
  `os.ReadDir` reports it as a link, which is not a directory.
- The bundled directory keeps its resolution (`prompts.SkillsDir()` beside the executable or the working
  directory) and stays gitignored: a checkout with no bundled skills lists only the project's, which is
  the state `docs/testing.md` describes for the mock LLM's plan-mode branch.

## Checking this

`go test ./backend/prompts/` fails if the frontmatter grammar, the one-level scan, the suffix rule or the
linked/oversized skip regresses.

- `go test ./backend/sandbox/` fails if the bound directory stops matching the scanned one.
  `TestProjectOptsCarriesTheProjectSkillDirectory`, `TestReadOnlyBindArgBindsOnlyWhatExists`.
- `go test ./backend/exec/` fails if a sandboxed prompt advertises a host path.
  `TestComposeSystemPrompt_ListsProjectSkills`.
