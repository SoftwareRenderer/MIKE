---
name: plan-first
description: Structured planning workflow for any coding task. Use at the start of every new feature, bug fix, refactor, or implementation request. Analyzes the project, asks up to 5 clarifying questions, creates a TODO.md, gets user approval, then executes task by task. Never writes code before a plan is approved.
---

# Plan-First Workflow

## Rules

- NEVER write code, create files, or run commands before a TODO.md is approved.
- NEVER assume missing information. Ask instead.
- NEVER skip steps. Follow phases in order.
- NEVER go off-plan. If new work is discovered, add it to TODO.md and ask for approval before doing it.

---

## Phase 1 — Analyze the Project

Read the project silently before asking anything. Check:

1. Directory structure (top 2 levels)
2. `package.json`, `go.mod`, `requirements.txt`, `Cargo.toml`, or equivalent
3. Existing dependencies and their versions
4. Build system and scripts (`Makefile`, `scripts/`, CI config)
5. `README.md` or `README.*`
6. Any existing `TODO.md`, `TASKS.md`, `.todo`, or open issue files

Do not output analysis results unless directly relevant to your questions.

---

## Phase 2 — Ask Clarifying Questions (One Round Only)

After analysis, identify gaps that would block correct implementation.

- Ask **at most 5 questions** in a single message.
- Only ask what is **critical and cannot be inferred** from the codebase.
- Number the questions.
- Do not ask about things already answerable from the project files.
- Do not split into multiple rounds — this is your only chance to ask.

Example format:

```
Before I create the plan, I need a few things clarified:

1. Should the new endpoint require authentication?
2. Is there a preferred database (the project has both SQLite and Postgres configs)?
3. Should existing tests be updated, or only new ones added?
```

Wait for the user's response before proceeding.

---

## Phase 3 — Create TODO.md

Using the analysis and the user's answers, write a `TODO.md` file in the project root.

### TODO.md Structure

```markdown
# TODO

## Goal
One sentence describing what will be built or fixed.

## Tasks

### 1. <Phase Name>
- [ ] <Concrete, measurable action>
- [ ] <Concrete, measurable action>

### 2. <Phase Name>
- [ ] <Concrete, measurable action>
- [ ] <Concrete, measurable action>

## Notes
Any constraints, decisions, or known risks recorded here.
```

### Requirements

- Tasks must be **small and independently verifiable** (one logical change each).
- Order tasks by **dependency** (prerequisites first).
- Each task must be checkable as done/not done.
- No vague items like "fix things" or "improve code".

After writing the file, show the full contents to the user and ask:

```
I've created TODO.md. Does this plan look correct?
Reply YES to start, or tell me what to change.
```

---

## Phase 4 — Revision Loop (if needed)

If the user requests changes:

1. Ask targeted follow-up questions to resolve the disagreement.
2. Rewrite `TODO.md`.
3. Show the updated plan and ask for approval again.

Repeat until the user approves.

---

## Phase 5 — Execute the Plan

Once approved:

1. Work through tasks **in order**, one at a time.
2. After completing each task, mark it done in `TODO.md`:
   - Change `- [ ]` to `- [x]`
3. State which task you are starting before you begin it.
4. Do not start the next task until the current one is complete.
5. Do not perform any work not listed in `TODO.md`.

If you discover that an unlisted task is required:
- Stop.
- Add it to `TODO.md` under a `## Discovered Tasks` section.
- Tell the user what was found and why it is needed.
- Ask for approval before continuing.

When all tasks are marked `[x]`, write:

```
All tasks in TODO.md are complete.
```