# ADR-0003 — A task fetches a submodule only under the project's two switches

**Status:** superseded by 0004

**Rules that follow:** `AGENTS.md → Git`, `docs/git-operations.md → Submodules`, `docs/api-contract.md → Submodules are read from the task and fetched by a click`

## Abstract

A task start may fetch a submodule, and does so only where the project's row says both that a turn may reach
the network and that a submodule URL may be dialed. A fetch reaches the network from the server process, over
an address that came out of repository text, spending the operator's credentials. Where either switch declines,
the task stops with the code `submodules_uninitialized` and the card answers with the URL and one button that
makes the fetch the person's own click.

## Context

MIKE fetched submodules at project import and nowhere else. That left every task on a repository with
submodules unable to start on a machine where import failed or ran before the submodule was added, and the
refusal told the person to run a command in a checkout they may not have on hand. Fetching from the task
instead is the obvious fix and the dangerous one: `.gitmodules` is text a repository ships, `git submodule
update --init` authenticates with `credential.helper` and the operator's ssh agent, and the fetch runs in the
server process — outside gVisor, outside the project's network isolation, and outside the tool path.

## Decision

`prepareSubmodules` (`backend/exec/submodules.go`) is asked before a task's container is built, and reads the
missing rows from the same walk that produced the refusal. `sandbox_network_isolation` is asked first and
outranks `auto_fetch_submodules`: a project whose turns reach no network reaches none for a submodule. The
stale-pointer fetch into the person's shared store asks the same pair, because it dials that store's remote
from the server process. Every fetch, a turn's or a click's, runs one level at a time under `fetchRounds` and
never with git's `--recursive`: a nested `.gitmodules` arrives inside a clone, so the addresses it names are
ones no screen has read. Anything
still unpopulated stops the task on `*git.SubmodulesUninitialized` — the row goes to Review, never to a retry,
because a retry cannot fix a checkout the person has to populate.

What the person clicks is the other half. `POST /api/projects/{projectID}/submodules/init` is
`InitSubmodules` over their own checkout, asked by a request rather than by a turn, so the credentials it spends
are spent on a decision somebody made in a browser. The listing beside it reads the task's commit for what to
show and the project's commit for what may be fetched: a gitlink only a task's commit names was written by the
model, and the model does not choose what MIKE downloads.

Every address a fetch may contact is screened before git is handed it, and refused where it names this machine
or a private network. It is screened on every reading, not the one `.gitmodules` shows: git dials the address
the checkout's own `.git/config` recorded and falls back to `.gitmodules` only without it, so the checkout's
answer and the store's `origin` are judged beside it. `docs/git-operations.md → Submodules` holds which
addresses, and `MIKE_ALLOW_PRIVATE_SUBMODULE_HOSTS` is the only widening.

## Consequences

A project on a machine with a git server on the LAN needs the operator's variable as well as the project's
switch, and a refusal says which one it is missing. A turn that would have fetched quietly now ends in Review
with an address in it, which is a person being asked to approve a destination they can read. The task cannot
answer the stop by itself, by design: the alternative is a server that dials a repository's chosen address
because a model wrote a gitlink. Import costs an extra listing per level, which is what a nested refusal is
worth: an address nobody read is not a clone worth having.

A checkout left holding an address its repository has since moved on from now stops with a sentence telling the
person to run `git submodule sync`, where it used to dial that address quietly. That is the intended cost of
screening what git will dial: an address nobody chose is not fetched just because a config file still names it.

What would reopen this is a per-fetch destination that a person pre-approved (an allowlist stored on the
project), which would let a turn fetch without a click while keeping the consent person-shaped. It is not built
because MIKE has one consent mechanism per decision today, and a project's row is it.
