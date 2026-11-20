# ADR-0004 — A task fetches no submodule; the person's click is the only fetch

**Status:** accepted

**Rules that follow:** `AGENTS.md → Git`, `docs/git-operations.md → Submodules`, `docs/api-contract.md → Submodules are read from the task and fetched by a click`

**Supersedes:** ADR-0003, whose premise was that a turn may fetch under two switches.

## Abstract

A task never fetches a submodule. A missing working copy stops the turn with the code
`submodules_uninitialized`, and the card's **Initialize submodules** button is the one thing that runs git's
fetch, into the person's own checkout. The project's row keeps no switch for it: `auto_fetch_submodules` is
gone from the schema, the API and the sandbox settings, and the chat is where that decision is made.

## Context

ADR-0003 gave a turn the fetch under `auto_fetch_submodules` and `sandbox_network_isolation`. The consent it
required was consent to a class — "this project's tasks may download a submodule" — taken once in the sandbox
form, long before anybody knew which repository, which address, or which task. The address itself was screened
before git dialed it, but the screen decides nothing about *wanting* the download, and a task that reached
Review only because a switch sat off asked the person to approve an address they had already, in some sense,
agreed to. Two mechanisms answering one decision is one too many, and the one worth keeping is the click that
names the address on the screen.

## Decision

`prepareSubmodules` (`backend/exec/submodules.go`) lists what the task's own commit names and the person's
checkout does not hold, and returns `*git.SubmodulesUninitialized` with those rows — path, redacted address,
MIKE's reason. It fetches nothing and asks no switch: neither of the project's two remaining switches is what
refused it, and the exec package holds no fetcher. `backend/git` keeps one verb for bringing a copy down,
`InitSubmodules`, reached by import and by `POST /api/projects/{projectID}/submodules/init`; a task reaches
neither.

`PopulateSubmodules`' stale-pointer fetch into the person's already-populated store stays, because that store
exists only where somebody already approved this submodule, and `allowStoreFetch` refuses it while the project
is isolated, since it dials the store's recorded remote from the server process.

## Consequences

One decision has one home: a turn that needs a submodule ends in Review naming it, and the only way past it is
the button, which spends the operator's credentials on a destination the person can read first. A project with
submodules costs one click per project, not one per task, because the fetch lands in the person's checkout and
every task's worktree is then a worktree of that store. The cost is that a project added before its submodule
was, or one whose import could not reach the upstream, cannot start such a task until a person is at the
browser — an unattended queue stalls there, deliberately.

The column, the two request fields and the sandbox-form row are gone with it, so a stored `1` from the era that
had it is dropped by the schema step rather than re-read under a new meaning.

What would reopen a turn-side fetch is a destination the person pre-approved and can read afterwards — an
allowlist stored on the project by address, not a switch over a class. It is not built because nothing today
needs an unattended submodule, and an approval that cannot name the address is the approval ADR-0003 could not
finish.

Re-measure with `go test ./backend/exec/ ./backend/git/` — `TestPrepareSubmodulesStopsNamingTheMissingCopyAndItsURL`
fails if a turn reaches the network again, in either isolation state, and
`TestPrepareSubmodulesStartsOnceThePersonPulledTheCopy` fails if the click stops being enough.
