# Implementation Progress — Kanban Board with Llama.cpp Integration

> **Test Endpoint**: `http://neptune:8010/upstream/Qwen3.6-35B-A3B/v1`
> **Last Updated**: 2026-06-02 (10:19 AM)
> **Status**: All phases complete. Backend builds, frontend builds, 34/34 tests pass.

---

## Phase 0: Project Setup & Infrastructure ✅

- [x] Create `go.mod` with module name and dependencies
  - [x] `github.com/coder/websocket`
  - [x] `modernc.org/sqlite`
- [x] Initialize frontend project (`npm create vite@latest` with Svelte + TypeScript)
- [x] Install frontend dependencies
  - [x] Svelte 5 (Runes)
  - [x] TypeScript
  - [x] Vite
  - [x] svelte-check, svelte-preprocess
  - [x] xterm.js (`@xterm/xterm`, `@xterm/addon-fit`)
  - [x] Markdown renderer (`marked`)
- [x] Create directory structure (`backend/`, `frontend/src/lib/components/`, `data/`, `worktrees/`)
- [x] Create `.gitignore`
- [x] SQLite schema creation with all tables and indexes
- [x] Config defaults initialization on first startup

---

## Phase 1: Backend Core ✅

- [x] HTTP server setup (`net/http`, custom mux with path params)
- [x] CORS middleware
- [x] JSON request/response encoding
- [x] Static file serving (built frontend)
- [x] SQLite store layer (`backend/store.go`)
  - [x] Project CRUD operations
  - [x] Task CRUD operations
  - [x] Conversation history read/write
  - [x] Config get/set
  - [x] Task dependency CRUD
  - [x] Review comment CRUD
  - [x] Home agent message CRUD
- [x] Global config handler (`backend/config.go`)
  - [x] GET `/api/config`
  - [x] PUT `/api/config`
  - [x] JSON serialization of `EndpointLimits` map
- [x] Health check endpoint (`GET /api/health`)

---

## Phase 2: Backend — Project Management ✅

- [x] GET `/api/projects` — list all projects
- [x] POST `/api/projects` — create project (validate path exists + is git repo)
- [x] GET `/api/projects/:id` — get project details
- [x] PUT `/api/projects/:id` — update project name
- [x] DELETE `/api/projects/:id` — delete project

---

## Phase 3: Backend — Board & Task CRUD ✅

- [x] GET `/api/projects/:id/board` — get board with tasks
- [x] PUT `/api/projects/:id/tasks/reorder` — bulk reorder tasks
- [x] POST `/api/projects/:id/tasks` — create task
- [x] GET `/api/projects/:id/tasks/:taskId` — get task
- [x] PUT `/api/projects/:id/tasks/:taskId` — update task
- [x] DELETE `/api/projects/:id/tasks/:taskId` — delete task (cascade cleanup)
- [x] POST `/api/projects/:id/tasks/:taskId/move` — move task to column

---

## Phase 4: Backend — Git Worktree Management ✅

- [x] Worktree creation from baseline branch
  - [x] Per-project worktree mutex (30s timeout)
  - [x] Uncommitted changes capture (stash → baseline branch → apply)
  - [x] Initial baseline commit snapshot
  - [x] Baseline branch cleanup
- [x] Worktree deletion (on task delete)
- [x] Branch naming: `task/<first-8-chars-of-uuid>`
- [x] Diff generation (`GET /api/projects/:id/tasks/:taskId/diff`)
- [x] Git endpoints
  - [x] GET `/api/projects/:id/git/log`
  - [x] GET `/api/projects/:id/git/branches`
  - [x] GET `/api/projects/:id/git/diff`
  - [x] GET `/api/projects/:id/git/status`
  - [x] POST `/api/projects/:id/git/checkout`
  - [x] POST `/api/projects/:id/git/pull`
  - [x] POST `/api/projects/:id/git/push`

---

## Phase 5: Backend — Llama.cpp Integration ✅

- [x] OpenAI-compatible HTTP client (`backend/llama.go`)
  - [x] Chat completions with streaming (`stream: true`)
  - [x] Parse SSE response tokens
  - [x] Parse native `tool_calls` from streaming response
  - [x] Fallback: parse tool calls from free-form text
  - [x] Connection retry with exponential backoff (3 attempts: 2s, 4s, 8s)
- [x] Tool definitions (read_file, write_file, execute_command, task_complete)
- [x] System prompt generation (with worktree path, tool definitions, timeout guidance)
- [x] Tool execution engine
  - [x] `read_file` — read from worktree with path validation
  - [x] `write_file` — write to worktree (create dirs, no symlink escapes)
  - [x] `execute_command` — run in worktree with timeout
    - [x] Command blacklist (sudo, rm -rf /, mkfs, dd, fork bombs, chmod 777 /)
    - [x] Timeout enforcement (default 5s, hard cap 300s)
  - [x] `task_complete` — signal completion with summary
- [x] Tool result feedback (feed results back to model as assistant tool responses)
- [x] Malformed JSON in tool arguments → tool failure + error feedback

---

## Phase 6: Backend — Task Execution Engine ✅

- [x] Multi-turn conversation orchestrator (`backend/execution.go`)
  - [x] context.Context for cancellation
  - [x] Conversation loop: send → stream → detect tool calls → execute → feed back
  - [x] Conversation history persistence to SQLite per turn
- [x] Status state machine
  - [x] idle → queued → running → awaiting_review → completed
  - [x] running → paused (on connection failure)
  - [x] running → failed (on max consecutive failed turns / user stop)
- [x] Task start endpoint (`POST /api/projects/:id/tasks/:taskId/start`)
- [x] Task stop endpoint (`POST /api/projects/:id/tasks/:taskId/stop`)
- [x] Task retry endpoint (`POST /api/projects/:id/tasks/:taskId/retry`)
  - [x] From In Progress (failed): load conversation history + continue message
  - [x] From Review (rejected): clear history + build review context
- [x] Task resume endpoint (`POST /api/projects/:id/tasks/:taskId/resume`)
- [x] Consecutive failed turns tracking
  - [x] Counter reset on success, increment on failure
  - [x] Auto-stop when counter >= task.MaxConsecutiveFailedTurns (default: 3)
- [x] Auto-commit on completion
  - [x] Generate commit message from summary
  - [x] `git add -A && git commit`
  - [x] Auto-PR branch creation (`pr/<branch>`)
  - [x] Auto-move to Done (skip Review)
- [x] Auto-move to Review when task_complete called (if auto_commit is false)
- [x] Dependent task auto-start on parent completion
- [x] Mid-stream disconnect handling
  - [x] Detect SSE stream break
  - [x] Retry with exponential backoff
  - [x] Persist partial conversation on failure → paused state

---

## Phase 7: Backend — Task Queue & Concurrency ✅

- [x] Per-endpoint concurrency limits from config
- [x] Task queue (FIFO) per endpoint
- [x] Queue dispatcher goroutine
  - [x] Monitor active task count vs limit
  - [x] Dequeue next task when slot available
  - [x] Broadcast queue position updates
- [x] Queue position tracking
- [x] Estimated wait time calculation (optional)

---

## Phase 8: Backend — WebSocket Streaming ✅

- [x] WebSocket upgrade handler (`github.com/coder/websocket`)
- [x] Fan-out broadcaster
  - [x] `map[string]*broadcaster` (task_id → broadcaster)
  - [x] Multiple client subscriptions per task
- [x] Ring buffer (last 100 events per broadcaster)
- [x] Background continuation (execution continues when all clients disconnect)
- [x] Reconnect buffer-sync protocol
  - [x] Client sends `{"type":"sync","seq":<last_seen_seq>}`
  - [x] Broadcaster responds with buffered events from seq+1
- [x] Broadcaster cleanup on terminal state
- [x] Task stream endpoint (`GET /api/projects/:id/tasks/:taskId/stream`)
- [x] Stream event types
  - [x] `token` — individual text tokens
  - [x] `reasoning` — reasoning content
  - [x] `reasoning_end` — end of reasoning block
  - [x] `tool_call` — AI tool request
  - [x] `tool_result` — tool execution result
  - [x] `status` — status/activity update
  - [x] `queue` — queue position update
  - [x] `complete` — task complete
  - [x] `error` — error event

---

## Phase 9: Backend — Task Dependencies ✅

- [x] GET `/api/projects/:id/tasks/:taskId/dependencies`
- [x] POST `/api/projects/:id/tasks/:taskId/dependencies` — create link
  - [x] DAG validation (detect circular dependencies)
- [x] DELETE `/api/projects/:id/tasks/:taskId/dependencies/:childTaskId`
- [x] GET `/api/projects/:id/tasks/:taskId/dependents`
- [x] Auto-start logic when parent task is accepted (moved to Done)

---

## Phase 10: Backend — Review Comments ✅

- [x] GET `/api/projects/:id/tasks/:taskId/review-comments`
- [x] POST `/api/projects/:id/tasks/:taskId/review-comments`
- [x] PUT `/api/projects/:id/tasks/:taskId/review-comments/:commentId`
- [x] DELETE `/api/projects/:id/tasks/:taskId/review-comments/:commentId`
- [x] Format review comments for retry context (`FILE: <path> (line <n>): <comment>`)

---

## Phase 11: Backend — Home Agent ✅

- [x] POST `/api/projects/:id/home-agent/message`
- [x] GET `/api/projects/:id/home-agent/messages`
- [x] GET `/api/projects/:id/home-agent/stream` (WebSocket)
  - [x] `user_message` → persist + trigger agent response
  - [x] `assistant_token` → streaming tokens
  - [x] `assistant_done` → response complete with stats
- [x] Home agent system prompt (board context, task creation capabilities)
- [x] Internal API call support (create tasks, link dependencies, start work)

---

## Phase 12: Backend — Terminal ✅

- [x] GET `/api/projects/:id/terminal` (WebSocket)
- [x] PTY session management
  - [x] Create PTY scoped to project root
  - [x] Forward stdin/stdout between WebSocket and PTY
  - [x] Handle resize events
- [x] JSON frame protocol
  - [x] Client → Server: `input`, `resize`
  - [x] Server → Client: `output`, `title`
- [x] Session cleanup on disconnect

---

## Phase 13: Frontend — Setup & Core ✅

- [x] Vite config (`vite.config.ts`)
  - [x] Svelte plugin
  - [x] Dev server proxy (`/api` → `localhost:3000`)
- [x] TypeScript types (`lib/types.ts`)
  - [x] Project, Board, Task, Config
  - [x] TaskDependency, ReviewComment, HomeAgentMessage
  - [x] GitCommit, GitBranch, GitStatus
  - [x] StreamEvent union type
  - [x] StreamSection model
- [x] API client (`lib/api.ts`)
  - [x] Project endpoints
  - [x] Board endpoints
  - [x] Task CRUD endpoints
  - [x] Task execution endpoints
  - [x] Config endpoints
  - [x] Dependency endpoints
  - [x] Review comment endpoints
  - [x] Git endpoints
  - [x] Home agent endpoints
  - [x] Terminal endpoint
  - [x] WebSocket stream client (`createTaskStream`)
- [x] Svelte stores (`lib/store.ts`)
  - [x] projects, selectedProject, sidebarCollapsed
  - [x] board, selectedTask, config
  - [x] activeStreams
- [x] CSS theme with design tokens (`app.css`)
  - [x] Color palette (surfaces, borders, text, status, columns)
  - [x] Typography, border radius
  - [x] Responsive breakpoints

---

## Phase 14: Frontend — Layout & Navigation ✅

- [x] `App.svelte` — root layout (sidebar + main area)
  - [x] Collapsible sidebar (240px → 48px)
  - [x] Top bar with project name + action buttons
  - [x] Main content area
- [x] Project navigation in sidebar
  - [x] Project list with active indicator
  - [x] Add Project button
  - [x] Collapse/expand toggle
- [ ] `ProjectSwitcher.svelte` (dropdown, Ctrl+Shift+P) — future enhancement
- [ ] `AddProjectDialog.svelte` (file picker) — simplified as prompt for now

---

## Phase 15: Frontend — Board Components ✅

- [x] `Board.svelte`
  - [x] 4-column flex layout
  - [x] Drag-and-drop handlers (dragstart, dragover, drop)
  - [x] Global controls (Add Task, Config buttons)
- [x] `Column.svelte`
  - [x] Column header with title + count badge
  - [x] Task list rendering
  - [x] Drop target highlighting
- [x] `Card.svelte`
  - [x] Draggable card surface
  - [x] Title + prompt preview truncation
  - [x] Status badges (running, queued, paused, review, failed)
  - [x] Quick-action buttons per column
    - [x] Backlog: Start button
    - [x] In Progress: Stop, Resume, Retry buttons
    - [x] Review: Move to Done button
  - [x] Click opens CardDetail (sets selectedTask store)
- [x] `CardDetail.svelte` (full slide-out panel)
  - [x] Slide-out sheet (right side, 480px max)
  - [x] Editable title
  - [x] Prompt section
  - [x] Stream section (embedded TaskStream when running)
  - [x] Output section (final summary)
  - [x] Diff section (git diff in Review)
  - [x] Metadata display
  - [x] Footer action buttons

---

## Phase 16: Frontend — Streaming Components ✅

- [x] `TaskStream.svelte`
  - [x] WebSocket connection management
  - [x] Section model (text, reasoning, tool_call)
  - [x] Auto-scroll hook
  - [x] Connection status bar
  - [x] Token statistics footer
- [x] `StreamSection.svelte`
  - [x] Collapsible wrapper
  - [x] Color-coded left border
  - [x] Section header with icon + title
- [x] `StreamActions.svelte`
  - [x] Copy to clipboard
  - [x] Toggle raw view
  - [x] Statistics badge

---

## Phase 17: Frontend — Task Management UI ✅

- [x] `CreateTaskModal.svelte`
  - [x] Sheet slide-out form
  - [x] Title input (required, max 200 chars)
  - [x] Prompt textarea (required, min 10 chars, auto-resize)
  - [x] Completion behavior section (auto-commit, auto-PR checkboxes)
  - [x] Ctrl+Enter submit, Escape close
- [ ] `TaskPrompt.svelte` (rich textarea for editing) — future enhancement
- [x] `ConfigModal.svelte`
  - [x] Dialog modal
  - [x] Server URL input
  - [x] Model name input
  - [x] Workspace directory input
  - [x] Endpoint concurrency limits (add/remove rows)
  - [x] Connection test button (ping health endpoint)

---

## Phase 18: Frontend — Terminal ✅

- [x] `TerminalPanel.svelte`
  - [x] xterm.js integration
  - [x] WebSocket connection to terminal endpoint
  - [x] Input/resize forwarding
  - [x] Output rendering
  - [x] Close button

---

## Phase 19: Testing & Integration

- [x] Unit tests — 34 tests, all passing
  - [x] Config tests (defaults, set, not found)
  - [x] Project CRUD tests (create, get, list, update, delete, duplicate path)
  - [x] Task CRUD tests (create, get, list, update, status state, delete, reorder)
  - [x] Cascade delete tests (project→task, task→conversation history, project→home agent messages)
  - [x] Conversation history tests (save, get, clear, cascade)
  - [x] Dependency tests (create, get, exists, delete, duplicate rejected)
  - [x] Review comment tests (create, get, unresolved, update, delete)
  - [x] Home agent message tests (create, get, cascade)
  - [x] Helper function tests (b2i, i2b)
  - [x] AutoCommit/AutoPR persistence test
- [ ] Test llama.cpp endpoint connectivity (`http://neptune:8010/upstream/Qwen3.6-35B-A3B/v1`)
- [ ] End-to-end task flow test
- [ ] WebSocket stream test
- [ ] Git worktree lifecycle test
- [ ] Dependency auto-start test
- [ ] Queue dispatcher test (concurrency limits)

---

## Build Status

| Component | Status | Output |
|-----------|--------|--------|
| Backend (Go) | ✅ Compiles | `kanban-server.exe` (16.8 MB) |
| Frontend (Svelte) | ✅ Builds | `frontend/dist/` (index.html + assets) |

---

## Notes

- **Backend working directory**: `m:\projects\kanban`
- **Database file**: `data/kanban.db`
- **Worktrees directory**: `./worktrees/`
- **Default backend port**: `3000`
- **Default frontend dev port**: `5173`
- **Test llama.cpp endpoint**: `http://neptune:8010/upstream/Qwen3.6-35B-A3B/v1`