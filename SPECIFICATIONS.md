# Kanban Board with Llama.cpp Integration - Specifications

## 1. Overview

A Kanban board application with 4 columns (Backlog → In Progress → Review → Done) that uses llama.cpp's OpenAI-compatible API to autonomously execute tasks. Each task is represented as a card containing a prompt/description. When moved to "In Progress", the backend spawns an AI task session that streams work back to the UI in real-time via WebSocket.

**Tech Stack:**
- **Backend**: Go (`github.com/coder/websocket`, `modernc.org/sqlite`)
- **Frontend**: Svelte + TypeScript + Vite
- **AI Engine**: llama.cpp (externally running server with OpenAI-compatible endpoints)
- **Persistence**: SQLite (`modernc.org/sqlite`)

---

## 2. Architecture

```
┌─────────────────────────────────────────────────────────┐
│                     Browser (Svelte)                    │
│  ┌──────────┐ ┌──────────┐ ┌──────────┐ ┌──────────┐    │
│  │ Backlog  │ │ In Progr.│ │ Review   │ │ Done     │    │
│  │ Column   │ │ Column   │ │ Column   │ │ Column   │    │
│  └──────────┘ └──────────┘ └──────────┘ └──────────┘    │
│         ↕ REST + WebSocket (fan-out)                     │
└───────────────────────────┬─────────────────────────────┘
                            │
┌───────────────────────────▼─────────────────────────────┐
│              Go Backend (Standard Library)              │
│  ┌────────────┐  ┌──────────────┐  ┌────────────────┐   │
│  │ HTTP Server│  │ Task Manager │  │ Git Worktree   │   │
│  │ (net/http) │  │ + WebSocket  │  │ Manager        │   │
│  └────────────┘  └──────────────┘  └────────────────┘   │
│  ┌────────────┐  ┌──────────────┐                       │
│  │ DB   Store │  │ Llama.cpp    │                       │
│  │ (Persist)  │  │ Client       │                       │
│  └────────────┘  └──────────────┘                       │
└───────────────────────────┬─────────────────────────────┘
                            │ OpenAI-compatible HTTP
┌───────────────────────────▼─────────────────────────────┐
│              llama.cpp Server (External)                │
│              (user runs separately)                     │
└─────────────────────────────────────────────────────────┘
```

---

## 3. Data Models

### 3.1 Task (Card)

```go
type Task struct {
    ID                        string    `json:"id"`                // UUID v4
    Title                     string    `json:"title"`             // Human-readable title
    Prompt                    string    `json:"prompt"`            // Detailed instructions for the AI
    Status                    string    `json:"status"`            // backlog, in_progress, review, done
    ColumnOrder               int       `json:"column_order"`      // Position within column (ascending)
    SessionID                 string    `json:"session_id"`        // AI session ID (set when task starts)
    Branch                    string    `json:"branch"`            // Git branch name (task/<short-id>)
    Output                    string    `json:"output"`            // Final AI output summary
    StatusState               string    `json:"status_state"`      // idle, queued, running, paused, awaiting_review, failed, completed
    LlamaServerURL            string    `json:"llama_server_url"`  // Per-task llama.cpp endpoint (inherits global if empty)
    ModelName                 string    `json:"model_name"`        // Per-task model override (inherits global if empty)
    MaxConsecutiveFailedTurns int       `json:"max_consecutive_failed_turns"` // Auto-stop after N consecutive failed tool calls (default: 3). A successful tool call resets the counter.
    AutoCommit                bool      `json:"auto_commit"`       // Automatically commit worktree changes when task completes
    AutoPr                    bool      `json:"auto_pr"`           // Automatically create a PR branch when task completes (implies auto_commit)
    PlanMode                  bool      `json:"plan_mode"`         // AI decomposes work into subtasks on the board, executes after user approval
    CreatedAt                 time.Time `json:"created_at"`
    UpdatedAt                 time.Time `json:"updated_at"`
}
```

### 3.2 Board

```go
type Board struct {
    Tasks []Task `json:"tasks"`
}
```

### 3.3 Server Configuration

```go
type Config struct {
    LlamaServerURL string         `json:"llama_server_url"` // e.g. "http://localhost:8081/v1"
    ModelName      string         `json:"model_name"`        // Optional: model name override
    WorkspaceDir   string         `json:"workspace_dir"`     // Root workspace for git worktrees
    EndpointLimits map[string]int `json:"endpoint_limits"`   // Per-endpoint concurrency limits, e.g. {"http://localhost:8081/v1": 2}
}
```

### 3.4 Task Move Request

```go
type MoveTaskRequest struct {
    Status string `json:"status"` // Target column: backlog, in_progress, review, done
}
```

### 3.5 Task Reorder Request

```go
type ReorderRequest struct {
    ID           string `json:"id"`
    ColumnOrder  int    `json:"column_order"`
}
```

### 3.6 Project

```go
type Project struct {
    ID        string    `json:"id"`         // UUID v4
    Name      string    `json:"name"`       // Human-readable name (derived from directory or user-set)
    Path      string    `json:"path"`       // Absolute filesystem path to the project directory (must be a git repository)
    CreatedAt time.Time `json:"created_at"`
    UpdatedAt time.Time `json:"updated_at"`
}
```

Each project has its own independent board (set of tasks). Git worktrees are shared across all projects under a single worktrees directory, configured via the `WORKTREES_DIR` environment variable (default: `./worktrees` relative to the Go server binary's working directory).

Projects are listed in alphabetical order (case-insensitive) via `ORDER BY name COLLATE NOCASE`.

### 3.7 Task Dependency

```go
type TaskDependency struct {
    ID            string    `json:"id"`             // UUID v4
    ParentTaskID  string    `json:"parent_task_id"` // Task that must complete first
    ChildTaskID   string    `json:"child_task_id"`  // Task that will auto-start
    AutoStart     bool      `json:"auto_start"`     // Whether child auto-starts when parent is accepted
    CreatedAt     time.Time `json:"created_at"`
}
```

Tasks can be linked in parent-child relationships. When a parent task is moved to Done (manually accepted or via auto-commit), all child tasks with `auto_start=true` are automatically started. Dependencies form a directed acyclic graph (DAG) — circular dependencies are rejected at creation time.

### 3.8 Review Comment

```go
type ReviewComment struct {
    ID        string    `json:"id"`          // UUID v4
    TaskID    string    `json:"task_id"`     // Associated task
    FilePath  string    `json:"file_path"`   // Path relative to worktree root
    LineNum   int       `json:"line_num"`    // 1-based line number in the file
    Comment   string    `json:"comment"`     // Review comment text
    Resolved  bool      `json:"resolved"`    // Whether the comment has been addressed
    CreatedAt time.Time `json:"created_at"`
    UpdatedAt time.Time `json:"updated_at"`
}
```

Review comments allow line-level feedback on task diffs. Comments are rendered alongside the diff in the UI and can be sent back to the AI as context when a task is rejected and retried.

### 3.9 Home Agent Message

```go
type HomeAgentMessage struct {
    ID        string    `json:"id"`          // UUID v4
    ProjectID string    `json:"project_id"`  // Associated project
    Role      string    `json:"role"`        // "user" or "assistant"
    Content   string    `json:"content"`     // Message text
    CreatedAt time.Time `json:"created_at"`
}
```

The home agent is a project-scoped conversational assistant accessible from the sidebar. It can help decompose work into tasks, create and link tasks, and start work on the board. Unlike task-scoped sessions, the home agent does not have its own worktree — it operates on the project's board state via internal API calls.

### 3.10 Git Commit (Response Type)

```go
type GitCommit struct {
    Hash    string `json:"hash"`    // Full commit hash
    Short   string `json:"short"`   // First 8 characters of hash
    Subject string `json:"subject"` // Commit message subject line
    Author  string `json:"author"`  // Author name
    Date    string `json:"date"`    // ISO 8601 timestamp
}
```

### 3.11 Git Branch (Response Type)

```go
type GitBranch struct {
    Name   string `json:"name"`   // Branch name (e.g., "main", "task/abc12345")
    Hash   string `json:"hash"`   // Commit hash the branch points to
    IsCurrent bool `json:"is_current"` // True if this is the current HEAD
}
```

### 3.12 Git Status (Response Type)

```go
type GitStatus struct {
    Branch     string   `json:"branch"`     // Current branch name
    Hash       string   `json:"hash"`       // Current HEAD commit hash
    Clean      bool     `json:"clean"`      // True if working tree has no changes
    Added      []string `json:"added"`      // New files staged
    Modified   []string `json:"modified"`   // Modified files staged or unstaged
    Deleted    []string `json:"deleted"`    // Deleted files
    Untracked  []string `json:"untracked"`  // Untracked files
}
```

### 3.13 Per-Task Configuration

Each task carries its own `LlamaServerURL` and `ModelName` fields (see Task struct above). These inherit from the global server configuration when empty, allowing different tasks to use different agents (different llama.cpp endpoints or models) without requiring per-project configuration.

### 3.14 WebSocket Message Types

```go
// WSMessage - Envelope for all WebSocket frames
type WSMessage struct {
    Type string `json:"type"` // token, reasoning, reasoning_end, tool_call, tool_result, status, queue, complete, error
    Data json.RawMessage `json:"data"`
}

// StreamTokenEvent - Individual text token from the AI
type StreamTokenEvent struct {
    Text string `json:"text"`
}

// StreamReasoningEvent - Reasoning/thinking content (collapsible in UI)
type StreamReasoningEvent struct {
    Text string `json:"text"` // Delta of reasoning tokens
}

// StreamReasoningEndEvent - Mark end of reasoning block
type StreamReasoningEndEvent struct {
    Summary string `json:"summary"` // Optional short summary to display in collapsed state
}

// StreamToolCallEvent - AI requests a tool invocation
type StreamToolCallEvent struct {
    ToolID   string                 `json:"tool_id"`   // Unique ID for this tool call
    ToolName string                 `json:"tool_name"` // "read_file", "write_file", "execute_command", "task_complete"
    Args     map[string]interface{} `json:"args"`      // Tool arguments
}

// StreamToolResultEvent - Result of a tool invocation
type StreamToolResultEvent struct {
    ToolID   string `json:"tool_id"`   // Matches the tool_call event
    Success  bool   `json:"success"`   // Whether the tool executed successfully
    Output   string `json:"output"`    // Tool output/result
    Duration int64  `json:"duration"`  // Execution time in milliseconds
}

// StreamStatusEvent - Task status/activity update
type StreamStatusEvent struct {
    State    string `json:"state"`    // running, queued, paused, awaiting_review, failed, completed
    Activity string `json:"activity"` // Human-readable activity description
}

// StreamQueueEvent - Queue position update for queued tasks
type StreamQueueEvent struct {
    Position        int `json:"position"`                 // Current position in queue (1-based)
    EstimatedWait   int `json:"estimated_wait_seconds"`   // Optional estimated wait time
}

// StreamCompleteEvent - Task execution finished
type StreamCompleteEvent struct {
    State   string `json:"state"`   // awaiting_review, failed, paused
    Summary string `json:"summary"` // Final summary of work done
    Tokens  struct {
        Prompt    int `json:"prompt"`
        Completed int `json:"completed"`
        Total     int `json:"total"`
    } `json:"tokens"`
    Duration int64 `json:"duration"` // Total execution time in milliseconds
}

// StreamErrorEvent - Error during execution
type StreamErrorEvent struct {
    Message string `json:"message"`
    Code    string `json:"code"` // Optional error code for categorization
}
```

---

## 4. Backend API

### 4.1 Project Endpoints

All board and task endpoints are scoped to a project via the `{projectId}` path parameter.

| Method | Endpoint                | Description              | Request Body         | Response                  |
|--------|-------------------------|--------------------------|----------------------|---------------------------|
| GET    | `/api/projects`         | List all projects (sorted alphabetically, case-insensitive) | — | `200 []Project` |
| POST   | `/api/projects`         | Create new project       | `{ path: string }`   | `201 Project`             |
| GET    | `/api/projects/:id`     | Get project details      | —                    | `200 Project` or `404`    |
| PUT    | `/api/projects/:id`     | Update project name      | `{ name: string }`   | `200 Project` or `404`    |
| DELETE | `/api/projects/:id`     | Delete project           | —                    | `204 No Content`          |

### 4.1a Filesystem Browser Endpoint

| Method | Endpoint       | Description                                              | Query Params          | Response                                                                                       |
|--------|----------------|----------------------------------------------------------|-----------------------|------------------------------------------------------------------------------------------------|
| GET    | `/api/fs`      | List directory contents for project directory selection  | `path` (string)       | `200 { path: string, isGitRepo: bool, entries: [{ name, isDir, path, isGitRepo }] }`          |

The response excludes hidden entries (names starting with `.`) and files (only directories are returned). Entries are sorted alphabetically (case-insensitive). Each directory entry includes an `isGitRepo` flag indicating whether it is a git repository. A `..` parent directory entry is included when not at root.

### 4.2 Board Endpoints (per-project)

| Method | Endpoint                         | Description              | Request Body         | Response                  |
|--------|----------------------------------|--------------------------|----------------------|---------------------------|
| GET    | `/api/projects/:id/board`        | Get project board with tasks | —                  | `200 Board`               |
| PUT    | `/api/projects/:id/tasks/reorder`| Bulk reorder tasks       | `[]ReorderRequest`   | `200 OK`                  |

### 4.3 Task CRUD Endpoints (per-project)

| Method | Endpoint                                | Description           | Request Body                    | Response                  |
|--------|-----------------------------------------|-----------------------|---------------------------------|---------------------------|
| POST   | `/api/projects/:id/tasks`               | Create new task       | `Task` (prompt, title?, llama_server_url?, model_name?, max_consecutive_failed_turns?, plan_mode?) | `201 Task` |
| GET    | `/api/projects/:id/tasks/:taskId`       | Get single task       | —                               | `200 Task` or `404`       |
| PUT    | `/api/projects/:id/tasks/:taskId`       | Update task fields    | `Task`                          | `200 Task` or `404`       |
| DELETE | `/api/projects/:id/tasks/:taskId`       | Delete task           | —                               | `204 No Content`          |
| POST   | `/api/projects/:id/tasks/:taskId/move`  | Move task to column   | `MoveTaskRequest`               | `200 Task` or `404`       |

**Auto-generated titles**: When creating a task, the `title` field is optional. If not provided, the backend auto-generates a title from the first line of the `prompt` (trimmed, truncated to 80 characters with `...` suffix if longer).

### 4.4 Task Execution Endpoints (per-project)

| Method | Endpoint                                    | Description                    | Response                  |
|--------|---------------------------------------------|--------------------------------|---------------------------|
| POST   | `/api/projects/:id/tasks/:taskId/start`     | Start AI task execution        | `200 Task` or `409 Conflict` |
| POST   | `/api/projects/:id/tasks/:taskId/stop`      | Stop a running AI task         | `200 Task` or `404`       |
| POST   | `/api/projects/:id/tasks/:taskId/retry`     | Retry a failed task (resumes conversation with additional context) | `200 Task` or `404` |
| POST   | `/api/projects/:id/tasks/:taskId/resume`    | Resume a paused task (reconnects to llama.cpp, loads conversation history) | `200 Task` or `404` |
| GET    | `/api/projects/:id/tasks/:taskId/stream`    | WebSocket stream for task output (HTTP Upgrade, fan-out: multiple clients can subscribe) | `WebSocket` |
| GET    | `/api/projects/:id/tasks/:taskId/diff`      | Get git diff for task worktree | `200 plain/text`          |

### 4.5 Configuration Endpoints

| Method | Endpoint       | Description                    | Request Body | Response          |
|--------|----------------|--------------------------------|--------------|-------------------|
| GET    | `/api/config`  | Get global server configuration | —           | `200 Config`      |
| PUT    | `/api/config`  | Update global server configuration | `Config` | `200 Config`      |

### 4.6 Task Dependency Endpoints (per-project)

| Method | Endpoint                                                             | Description                        | Request Body                                              | Response                          |
|--------|----------------------------------------------------------------------|------------------------------------|-----------------------------------------------------------|-----------------------------------|
| GET    | `/api/projects/:id/tasks/:taskId/dependencies`                       | Get dependencies for a task        | —                                                         | `200 []TaskDependency`            |
| POST   | `/api/projects/:id/tasks/:taskId/dependencies`                       | Link two tasks (create dependency) | `{ child_task_id: string, auto_start: bool }`             | `201 TaskDependency` or `409`     |
| DELETE | `/api/projects/:id/tasks/:taskId/dependencies/:childTaskId`          | Remove a dependency                | —                                                         | `204 No Content`                  |
| GET    | `/api/projects/:id/tasks/:taskId/dependents`                         | Get tasks that depend on this task | —                                                         | `200 []TaskDependency`            |

### 4.7 Review Comment Endpoints (per-project)

| Method | Endpoint                                                         | Description                        | Request Body                                              | Response                          |
|--------|------------------------------------------------------------------|------------------------------------|-----------------------------------------------------------|-----------------------------------|
| GET    | `/api/projects/:id/tasks/:taskId/review-comments`                | Get review comments for a task     | —                                                         | `200 []ReviewComment`             |
| POST   | `/api/projects/:id/tasks/:taskId/review-comments`                | Add a review comment               | `{ file_path: string, line_num: int, comment: string }`   | `201 ReviewComment`               |
| PUT    | `/api/projects/:id/tasks/:taskId/review-comments/:commentId`     | Update a review comment            | `{ comment?: string, resolved?: bool }`                   | `200 ReviewComment`               |
| DELETE | `/api/projects/:id/tasks/:taskId/review-comments/:commentId`     | Delete a review comment            | —                                                         | `204 No Content`                  |

### 4.8 Git Endpoints (per-project)

| Method | Endpoint                                                         | Description                        | Request Body                                              | Response                          |
|--------|------------------------------------------------------------------|------------------------------------|-----------------------------------------------------------|-----------------------------------|
| GET    | `/api/projects/:id/git/log`                                      | Get commit log                     | `{ branch?: string, limit?: int }`                        | `200 []GitCommit`                 |
| GET    | `/api/projects/:id/git/branches`                                 | List branches                      | —                                                         | `200 []GitBranch`                 |
| GET    | `/api/projects/:id/git/diff`                                     | Get diff between refs              | `{ ref: string, path?: string }`                          | `200 plain/text`                  |
| GET    | `/api/projects/:id/git/status`                                   | Get working tree status            | —                                                         | `200 GitStatus`                   |
| POST   | `/api/projects/:id/git/checkout`                                 | Checkout a branch                  | `{ branch: string }`                                      | `200 OK`                          |
| POST   | `/api/projects/:id/git/pull`                                     | Pull from remote                   | `{ branch?: string }`                                     | `200 OK`                          |
| POST   | `/api/projects/:id/git/push`                                     | Push to remote                     | `{ branch?: string }`                                     | `200 OK`                          |

### 4.9 Home Agent Endpoints (per-project)

| Method | Endpoint                                                         | Description                        | Request Body                                              | Response                          |
|--------|------------------------------------------------------------------|------------------------------------|-----------------------------------------------------------|-----------------------------------|
| POST   | `/api/projects/:id/home-agent/message`                           | Send a message to the home agent   | `{ content: string }`                                     | `200 HomeAgentMessage`            |
| GET    | `/api/projects/:id/home-agent/messages`                          | Get home agent conversation        | —                                                         | `200 []HomeAgentMessage`          |
| GET    | `/api/projects/:id/home-agent/stream`                            | WebSocket stream for home agent    | —                                                         | `WebSocket`                       |

### 4.10 Terminal Endpoints (per-project)

| Method | Endpoint                                              | Description              | Response        |
|--------|-------------------------------------------------------|--------------------------|-----------------|
| GET    | `/api/projects/:id/terminal`                          | WebSocket terminal session | `WebSocket`   |

### 4.11 Health Check

| Method | Endpoint    | Description | Response        |
|--------|-------------|-------------|-----------------|
| GET    | `/api/health` | Health check | `200 {"status":"ok"}` |

---

## 5. Llama.cpp Integration

### 5.1 Connection

- Backend connects to an externally running llama.cpp server via its OpenAI-compatible HTTP API
- Default URL: `http://localhost:8081/v1`
- Configurable via `/api/config` endpoint and `data/config.json`
- Per-task override: each task can specify its own `LlamaServerURL` and `ModelName` (inherits global defaults when empty)

### 5.2 API Calls

**Chat Completions (Streaming with Tool Calling):**
```
POST {llama_server_url}/chat/completions
Content-Type: application/json

{
    "model": "default",
    "messages": [
        {
            "role": "system",
            "content": "SYSTEM_PROMPT"
        },
        {
            "role": "user",
            "content": "TASK_PROMPT"
        }
    ],
    "stream": true,
    "temperature": 0.7,
    "max_tokens": 4096,
    "tools": [
        { "type": "function", "function": { "name": "read_file", "parameters": { ... } } },
        { "type": "function", "function": { "name": "write_file", "parameters": { ... } } },
        { "type": "function", "function": { "name": "execute_command", "parameters": { ... } } },
        { "type": "function", "function": { "name": "task_complete", "parameters": { ... } } }
    ]
}
```

**Response (SSE from llama.cpp with native tool_calls):**
```
data: {"choices":[{"delta":{"content":"Hello"},"finish_reason":null}]}
data: {"choices":[{"delta":{"tool_calls":[{"function":{"name":"read_file","arguments":"..."}}]},"finish_reason":null}]}
data: {"choices":[{"delta":{},"finish_reason":"stop"}]}
data: [DONE]
```

The primary parsing path uses the native OpenAI `tool_calls` format from the streaming response. A secondary fallback parser extracts tool calls from free-form text when the model doesn't support structured tool calling.

### 5.3 System Prompt

The system prompt instructs the AI to act as an autonomous coding assistant:

```
You are an autonomous coding assistant working on a task in a git worktree.
Your working directory is: {worktree_path}

You have access to the following tools via function calling:
- read_file(path): Read the contents of a file
- write_file(path, content): Write content to a file (creates directories as needed)
- execute_command(command, timeout): Execute a shell command in the worktree
- task_complete(summary): Signal that the task is complete with a summary of work done

When the task is complete, call task_complete() with a summary including:
- Files created or modified
- Commands executed
- Any issues encountered

For the execute_command tool, always set the 'timeout' parameter:
- 2s for fast commands (ls, cat, grep, git status)
- 10s for package installs (npm install, pip install)
- 30s for builds and tests (npm run build, npm test, make)
- 60s for long-running operations (migrations, large downloads)
Timeout is specified in seconds. The server will reject values above 300s.
```

#### 5.3a Plan Mode System Prompt

When `task.PlanMode` is `true`, an extended system prompt is used that adds a structured planning workflow after the standard instructions:

```
## PLAN MODE — Follow this workflow strictly:

### Phase 1: Analyze the Project
Read the project structure silently. Check directory layout, config files (package.json, go.mod, etc.),
dependencies, build system, README, and any existing TODO or issue files.

### Phase 2: Ask Clarifying Questions
Identify gaps that would block correct implementation. Ask at most 5 critical questions in a single message.
Do not ask about things already answerable from the codebase.

### Phase 3: Decompose Into Subtasks
Use the `create_task(title, prompt)` tool to break the work into a tree of subtasks on the Kanban board.
Each subtask should be small and independently verifiable. Use `parent_task_id` for nested subtasks.
After creating all subtasks, call task_complete() with summary: "Plan created with N subtasks. Awaiting approval to proceed."

### Phase 4: Execute the Plan
Once the user approves the plan (by moving the task to In Progress), subtasks will be executed
automatically. The AI does NOT need to execute subtasks itself — that is handled by the system.
```

### 5.4 Task Execution Flow

```
1. User moves card to "In Progress" (drag-drop) or clicks "Start Task" button
       │
       ▼
2. Backend checks endpoint concurrency limit (from config.EndpointLimits).
   If the endpoint is not in config.EndpointLimits, default to 2 concurrent tasks.
   - If slot available: proceed to step 3
   - If at limit: task status_state → "queued", add to FIFO queue,
     broadcast queue position to WebSocket clients
       │
       ▼
3. Backend validates: task not already running, llama.cpp configured
   (use task's LlamaServerURL/ModelName, or fall back to global config)
       │
       ▼
4. Backend creates git worktree: ./worktrees/<task-id>/
    - Acquires per-project worktree mutex (30s timeout, returns 503 if locked)
    - Captures uncommitted working tree changes by creating a temporary
      baseline commit on a UUID-based internal branch `__kanban_baseline_<task-id>`:
      a) Stash current working tree changes (if any)
      b) Create branch `__kanban_baseline_<task-id>` from current HEAD
      c) Apply stashed changes and commit on that baseline branch
      d) If no uncommitted changes, use current HEAD directly
    - Create worktree from the baseline branch (or HEAD if clean)
    - Initial commit snapshot captured for diff baseline
    - Delete `__kanban_baseline_<task-id>` branch (cleanup within mutex scope)
    - Release per-project worktree mutex
    - Branch: task/<first-8-chars-of-uuid>
       │
       ▼
5. Backend loads conversation history from SQLite (empty on first run,
   persisted messages on retry/resume)
       │
       ▼
6. Backend connects to llama.cpp /v1/chat/completions with stream=true
   - On connection failure: retry with exponential backoff (3 attempts: 2s, 4s, 8s)
   - After 3 failures: status_state → "paused", await user resume
   - System prompt with worktree path and tool definitions (including task_complete)
   - User message = task prompt (+ conversation history on retry)
       │
       ▼
7. Backend streams tokens to frontend via WebSocket (/api/projects/:id/tasks/:taskId/stream)
   - Fan-out broadcaster: all connected clients receive the same stream events
   - Parses native OpenAI tool_calls format from model response (primary)
   - Fallback: parse tool calls from free-form text
   - Executes tools and feeds results back to the model
   - Persists each turn's messages to conversation_history table after completion
   - Tracks consecutive failed tool calls:
      * Successful tool call → reset counter to 0
      * Failed tool call → increment counter
      * Counter >= MaxConsecutiveFailedTurns → status_state → "failed"
   - Continues multi-turn conversation until:
      a) The AI calls `task_complete()` tool, OR
      b) Max consecutive failed turns reached (`task.MaxConsecutiveFailedTurns`, default: 3)
       │
       ▼
8. On completion (task_complete called):
   - If task.AutoCommit == true:
      a) Generate commit message from task summary
      b) Run `git add -A && git commit -m "<summary>"` in the worktree
      c) If task.AutoPr == true: create a PR branch named `pr/<task-branch>`
      d) Task status_state → "completed", auto-move to "Done" column
      e) Check for dependent tasks (task_dependencies where parent_task_id = this task and auto_start = true),
         automatically start each child task (go to step 1)
      f) Skip review column entirely
   - If task.AutoCommit == false:
      a) Task status_state → "awaiting_review", auto-moves to "Review" column
        │
        ▼
9. On task moved to Done (manual accept in Review column):
   - Task status_state → "completed"
   - Check for dependent tasks (task_dependencies where parent_task_id = this task and auto_start = true),
     automatically start each child task (go to step 1)
        │
        ▼
10. On max consecutive failed turns reached:
    - Task status_state → "failed"
    - Task output = "Max consecutive failed turns ({N}) reached"
    - Task remains in "In Progress" (user can retry or manually move)
        │
        ▼
11. On error (non-retryable, e.g., invalid configuration):
    - Task status_state → "failed"
    - Task output = error message
    - Task remains in "In Progress" (user can retry)
        │
        ▼
12. On user stop (POST /api/projects/:id/tasks/:taskId/stop):
    - Cancel execution context, terminating the llama.cpp stream
    - Task status_state → "failed"
    - Task output = "Stopped by user"
    - Preserve conversation history in SQLite (available for retry)
    - Preserve git worktree (available for inspection)
    - Task remains in "In Progress" (user can retry or manually move)
       │
       ▼
13. On mid-stream disconnect (llama.cpp SSE stream breaks during token generation):
    - Retry with exponential backoff (3 attempts: 2s, 4s, 8s)
    - On each retry: reconnect to llama.cpp, send full conversation history
      (including partial assistant message) to resume from last turn
    - After 3 failures: status_state → "paused", await user resume
    - Persist partial conversation to SQLite before marking paused
        │
        ▼
14. On retry from In Progress (failed state, POST /api/projects/:id/tasks/:taskId/retry):
    - Load persisted conversation history from SQLite
    - Add context message: "Your previous execution was interrupted.
      Please review your partial work and continue. If the task is already
      complete, call task_complete(). Otherwise, finish the remaining work."
    - Reuse the existing git worktree (preserve partial work)
    - Reset consecutive failed turns counter to 0
    - Reset status_state → "running"
        │
        ▼
15. On retry from Review column (user rejects, moves back to In Progress):
    - Clear persisted conversation history (fresh start)
    - Build review context from unresolved ReviewComment entries for this task
    - Add context message: "You previously attempted this task but the
      result was rejected. Review the worktree state and improve the implementation.
      The previous AI summary was: '{task.output}'.
      The following review comments must be addressed:
      {formatted_review_comments}"
    - Review comments are formatted as: "FILE: <file_path> (line <line_num>): <comment>"
    - Reuse the existing git worktree (preserve completed work for inspection)
    - Reset status_state → "running"
        │
        ▼
16. On resume (from paused state):
    - Reconnect to llama.cpp endpoint
    - Load persisted conversation history from SQLite
    - Continue from last known state
    - Reset status_state → "running"
        │
        ▼
17. Task Queue Dispatcher (background goroutine):
    - Monitors per-endpoint active task count vs. configured limit
    - When a running task completes/fails/pauses: decrement endpoint counter
    - If counter < limit and queue non-empty: dequeue next task (FIFO),
      start execution, broadcast status change to all WebSocket clients
```

### 5.5 Tool Calling

The backend supports four tools that the AI can request:

| Tool | Parameters | Description |
|------|-----------|-------------|
| `read_file` | `path` (string) | Read file contents from worktree |
| `write_file` | `path` (string), `content` (string) | Write file to worktree (creates directories as needed) |
| `execute_command` | `command` (string), `timeout` (int, optional) | Run shell command in worktree. `timeout` is model-specified in seconds (default: 5s if omitted, server-side hard cap: 300s). The model should set a low timeout for fast commands (e.g., `ls`) and higher for builds/tests. Server-side blocking of unsafe commands. |
| `task_complete` | `summary` (string) | Signal task completion with a work summary |

**Tool Execution Safety:**
- `execute_command`: Working directory always set to the worktree root
- Timeout: Model-specified via `timeout` parameter in the tool call. Default: 5 seconds if not specified. Server-side hard cap: 300 seconds (5 minutes) — any model-specified value above this is clamped to the cap. The model is instructed to set appropriate timeouts per command (e.g., 2s for `ls`, 30s for `npm test`).
- Server-side hardcoded blacklist of unsafe command patterns:
  - `sudo` — privilege escalation
  - `rm -rf /` or `rm -rf /*` — destructive root deletion
  - `mkfs.*` — filesystem formatting
  - `dd if=/dev/` — raw device access
  - `:(){ :|:& };:` — fork bombs
  - `chmod -R 777 /` — recursive root permission changes
- All tool file paths are validated to remain within the worktree directory (no symlink escapes or path traversal)

**Tool Call Detection:**
- Primary: Parse native OpenAI `tool_calls` array from streaming SSE response
- Fallback: Parse structured JSON tool calls from free-form text (for models without native support)
- If JSON in tool arguments is malformed: treat as a tool failure with error message "Invalid JSON in tool arguments", feed error back to the model, and increment the consecutive failed turns counter
- Tool results are fed back as assistant tool responses in the conversation history

**Consecutive Failed Turn Tracking:**
- Each tool call result is tracked: success resets the consecutive failure counter to 0, failure increments it by 1.
- When the counter reaches `task.MaxConsecutiveFailedTurns` (default: 3), execution stops and the task transitions to `failed` state.
- This allows the AI to make many tool calls overall but auto-stops when it enters a failure loop.

### 5.6 Conversation History

- **SQLite persistence**: Conversation history (full message arrays including tool calls and results) is persisted to the `conversation_history` table in SQLite after each turn completes.
- **Survives restarts**: If the server restarts mid-execution, the task is marked as `paused`. The conversation history is preserved in the database and can be loaded on resume.
- **Retry context (from In Progress/failed)**: On retry, the full persisted conversation history is loaded from SQLite and re-sent to the model with an additional system message: "Your previous execution was interrupted. Please review your partial work and continue. If the task is already complete, call task_complete(). Otherwise, finish the remaining work."
- **Retry context (from Review/rejected)**: When a task is moved from Review back to In Progress, conversation history is cleared and a fresh start is made. The re-prompt includes the previous AI summary and all unresolved review comments formatted as: "FILE: <file_path> (line <line_num>): <comment>". This ensures the AI can address specific line-level feedback from the reviewer.
- **Cleanup**: Conversation history is automatically deleted (via FOREIGN KEY CASCADE) when the associated task is deleted. Worktree artifacts and conversation history are NOT purged when a task is moved to Done, allowing post-completion inspection.

---

## 6. Frontend (Svelte)

### 6.1 Project Structure

```
frontend/
├── src/
│   ├── main.ts
│   ├── app.html                 (Vite template)
│   ├── lib/
│   │   ├── api.ts               // REST client + WebSocket stream client
│   │   ├── store.ts             // Svelte writable stores
│   │   ├── types.ts             // TypeScript interfaces
│   │   └── components/
│   │       ├── App.svelte            // Root layout with sidebar + board
│   │       ├── FileBrowser.svelte    // Directory browser for project selection
│   │       ├── Board.svelte          // Main board layout (4 columns)
│   │       ├── Column.svelte         // Single column with header + card list
│   │       ├── Card.svelte           // Draggable task card with status badge
│   │       ├── CardDetail.svelte     // Slide-out detail panel (Sheet component)
│   │       ├── TaskStream.svelte     // Live WebSocket streaming output (agentic view)
│   │       ├── StreamSection.svelte  // Collapsible section (reasoning/tool/text)
│   │       ├── StreamToolCall.svelte // Tool call display with loading/result states
│   │       ├── StreamReasoning.svelte// Collapsible reasoning/thinking block
│   │       ├── StreamText.svelte     // Markdown-rendered text content
│   │       ├── StreamActions.svelte  // Copy, export, statistics action icons
│   │       ├── CreateTaskModal.svelte// New task dialog (Sheet/Dialog component)
│   │       ├── TaskPrompt.svelte     // Rich textarea input with auto-resize
│   │       └── ConfigModal.svelte    // llama.cpp config dialog
│   └── app.css
├── static/
├── package.json
├── svelte.config.js
├── tsconfig.json
└── vite.config.ts
```

### 6.2 Components

All components follow the design patterns and input/presentation features from the llama.cpp UI (`./examples/llama.cpp/tools/ui`), using the same component primitives (Sheet, Dialog, Badge, Button, Collapsible, ScrollArea, etc.).

**Board.svelte**
- Props: `tasks`, `projectId`, `onAddTask?`
- Renders 4 Column components in a horizontal flex layout
- Handles drag-and-drop at the board level (dragstart, dragover, drop)
- Passes `onAddTask` callback to the Backlog column (renders "+ Add Task" button there)
- Subscribes to board store for reactive updates
- Responsive: collapses to single-column scroll on mobile

**Column.svelte**
- Props: `columnId`, `title`, `tasks`, `color`, `onDragStart`, `onDrop`, `onAddTask?`
- Renders column header with title and task count badge (Badge component)
- Backlog column: shows "+ Add Task" button at the top of the card list (when `onAddTask` is provided)
- Renders Card components in order
- Handles drop events to reorder/move tasks
- Visual drop-target highlighting with border animation

**Card.svelte**
- Props: `task`, `index`, `columnId`
- Surface: `--color-surface-2` background with `--color-border` border, `--radius-md` corners
- Displays: title (truncated to 1 line), truncated prompt preview (2 lines), status badge
- Status badges (Badge component with color-coded variants):
  - Blue spinner + "Running" (status_state: running)
  - Orange spinner + "Queued (#N)" (status_state: queued) — shows position in queue
  - Purple + "Paused" (status_state: paused)
  - Green check + "Review" (status_state: awaiting_review)
  - Red X + "Failed" (status_state: failed)
  - None (idle/backlog/done)
- Draggable with native HTML5 drag-and-drop (`draggable="true"`)
- Click opens CardDetail slide-out panel
- Hover reveals quick-action buttons per column:
  - Backlog: Play/Start icon button ("Start Task" — triggers start + auto-move to In Progress)
  - In Progress: Stop icon button (if running), Resume icon button (if paused), Retry icon button (if failed)
  - Review: Move to Done icon button
  - Done: Hidden (artifacts preserved for inspection via CardDetail)

**CardDetail.svelte**
- Sheet (slide-out panel) from the right side, full-height, width: 480px max
- Header: task title (editable inline), close button, status badge
- Body sections:
  - **Prompt Section**: full prompt text displayed in a prose/MarkdownContent block
  - **Stream Section** (if running/paused/queued): embedded TaskStream component with live output
  - **Output Section** (if completed/awaiting_review): final summary in MarkdownContent
  - **Diff Section** (if in Review): git diff view with syntax highlighting
  - **Metadata**: created/updated dates, branch name, session ID, agent config (model/endpoint)
- Footer: action buttons (Start, Stop, Resume (if paused), Retry (if failed), Move to column, Delete)
- Uses ScrollArea for scrollable body content

**TaskStream.svelte**
- Connects to `/api/projects/:id/tasks/:taskId/stream` via WebSocket
- Connection initiated when the user opens CardDetail for a running task (not automatically on task start)
- Supports multiple simultaneous clients (fan-out from backend broadcaster)
- Renders structured agentic sections (inspired by `ChatMessageAgenticContent`):
  - **Text sections**: streamed tokens rendered as MarkdownContent with auto-scroll
  - **Reasoning sections**: collapsible blocks with thinking icon, pending state while streaming, summary in collapsed state
  - **Tool call sections**: structured display showing tool name, arguments, loading state, and result
- Auto-scrolls to bottom as new content arrives (with `use-auto-scroll` hook pattern)
- Connection status bar at top (connecting/connected/disconnected)
- Footer with token statistics (prompt tokens, completed tokens, duration)
- Action icons: copy output, toggle raw view, collapse all reasoning

**StreamSection.svelte**
- Generic collapsible section wrapper for stream content
- Props: `type` (reasoning | tool_call | text), `collapsible`, `collapsed`
- Renders section header with icon, title, and collapse toggle
- Uses Collapsible component for expand/collapse animation
- Color-coded left border accent per type

**StreamToolCall.svelte**
- Displays a single tool invocation in a structured card
- States:
  - **Pending**: tool name + arguments in monospace block + loading spinner
  - **Success**: green check + output preview (collapsible) + duration badge
  - **Error**: red X + error message in red text
- Tool name displayed as a Badge with icon (file for read/write, terminal for execute_command, check for task_complete)
- Arguments rendered as formatted JSON in a monospace code block
- Output rendered in a scrollable monospace container with max-height

**StreamReasoning.svelte**
- Collapsible reasoning/thinking block
- Header: thinking icon + "Reasoning" label + token count
- Pending state: pulsing animation while reasoning tokens stream in
- Collapsed state: shows summary line (e.g., "Thinking... (127 tokens)")
- Expanded state: full reasoning text in italic, muted color (`--color-text-tertiary`)
- Uses Collapsible component with smooth height animation

**StreamText.svelte**
- Renders markdown-formatted text content
- Supports code blocks with syntax highlighting (monospace, dark background)
- Auto-scroll container for streaming content
- Typography: prose-like styling with proper headings, lists, links

**StreamActions.svelte**
- Action icon bar displayed at the bottom of completed stream output
- Actions:
  - Copy to clipboard (copies full stream text)
  - Toggle raw output view (shows raw JSON/stream frames)
  - Statistics badge (tokens, duration)
- Uses the same icon-button pattern as `ChatMessageActionIcons`

**CreateTaskModal.svelte**
- Dialog modal (centered overlay) for creating new tasks
- Form layout:
  - Description textarea (required) — the only required field. Placeholder: "Describe the task the AI should execute..."
  - Title is NOT entered by the user — it is auto-generated by the backend from the first line of the prompt (trimmed, truncated to 80 chars with `...` if longer)
  - Options section:
    - "Plan mode" checkbox (default: off) — AI decomposes work into subtasks on the board, then executes after user approval
    - "Auto-commit on completion" checkbox (default: off) — when checked, the AI's work is automatically committed and the task moves to Done
    - "Auto-create PR branch" checkbox (default: off, requires auto-commit) — creates a `pr/<branch>` copy when the task completes
- Submit button: disabled when form is invalid or loading
- Keyboard shortcuts: Ctrl+Enter to submit, Escape to close

**TaskPrompt.svelte** (used in CardDetail for editing)
- Rich textarea with auto-resize height (inspired by `ChatFormTextarea`)
- Placeholder text, disabled state, character limit
- Keyboard shortcuts: Ctrl+Enter to save, Escape to cancel
- Auto-focus on mount

**ConfigModal.svelte**
- Dialog modal (centered overlay) with structured form
- Form fields:
  - llama.cpp Server URL input (default: http://localhost:8081/v1)
  - Model Name input (optional, placeholder: "Leave empty for default")
  - Workspace Directory input (default: ./workspace)
  - Endpoint Concurrency Limits section:
    - Key-value pairs: endpoint URL → max concurrent tasks
    - Add/Remove rows for different llama.cpp endpoints
    - Example: `http://localhost:8081/v1` → `2`
- Save button with loading state
- Connection test button: pings llama.cpp health endpoint, shows success/failure result
- Uses Input, Button, and Alert UI primitives

**App.svelte**
- Root layout component with collapsible sidebar + main content area
- Sidebar: project list (sorted alphabetically, case-insensitive) with remove buttons
- Main area: top bar with project name + settings, then the Board component
- Modals: CreateTaskModal, ConfigModal, TerminalPanel, CardDetail, FileBrowser
- On first load: fetches project list, selects first project
- Layout: flex row, sidebar width ~240px (collapsible to 48px icon-only mode)
- Error toasts displayed at bottom center for operation failures

**Sidebar (in App.svelte)**
- Collapsible sidebar displaying project list (sorted alphabetically, case-insensitive)
- Each project rendered as a list item with:
  - Project name (truncated with ellipsis)
  - Active indicator (highlighted background when selected)
  - Remove button (✕) visible on hover — triggers confirmation dialog before removal
- "Add Project" button at the bottom (opens FileBrowser)
- Toggle button to collapse/expand sidebar (48px icon-only mode when collapsed)
- Projects are removed via `DELETE /api/projects/:id` with auto-selection of next project

**ProjectSwitcher.svelte**
- Top bar dropdown showing current project name
- Click to open dropdown with all projects listed
- Each project in dropdown shows name and path (truncated)
- "Add new project..." option at the bottom
- Keyboard shortcut: Ctrl+Shift+P to open switcher
- Uses DropdownMenu pattern from llama.cpp UI

**FileBrowser.svelte**
- Dialog modal for browsing and selecting a project directory
- Path bar: home button (🏠), editable path input (Enter to navigate)
- Directory listing: alphabetically sorted (case-insensitive), excludes hidden entries (`.` prefix) and files
- Each directory entry shows a `git` badge if it is a git repository
- Clicking a git repo directory selects it immediately (adds the project)
- Clicking a non-git directory navigates into it
- `..` parent entry for navigation (when not at root)
- Footer shows current path, Cancel button, and Select button (enabled when current path is a git repo)
- Calls `GET /api/fs?path=<path>` to fetch directory contents

### 6.3 State Management

```typescript
// stores.ts
import { writable } from 'svelte/store';

// Project state
export const projects = writable<Project[]>([]);
export const selectedProject = writable<Project | null>(null);
export const sidebarCollapsed = writable<boolean>(false);

// Board state (scoped to selected project)
export const board = writable<Board | null>(null);
export const selectedTask = writable<string | null>(null);
export const config = writable<Config | null>(null);
export const activeStreams = writable<Map<string, WebSocket>>(new Map());
```

### 6.4 Drag and Drop

Uses native HTML5 Drag and Drop API (no external library):

- `draggable="true"` on Card elements
- `dragstart`: Set data transfer with task ID and source column
- `dragover`: Prevent default, highlight drop target column
- `drop`: Call PUT `/api/projects/:id/tasks/reorder` or POST `/api/projects/:id/tasks/:taskId/move`

### 6.5 Styling

CSS custom properties for theming, inspired by the example UI in `./examples/llama.cpp/tools/ui`:

```css
:root {
    /* Surface hierarchy */
    --color-surface-0: #1F2428;
    --color-surface-1: #24292E;
    --color-surface-2: #2D3339;
    --color-surface-3: #353C43;
    --color-surface-4: #3E464E;

    /* Borders */
    --color-border: #30363D;
    --color-border-bright: #444C56;

    /* Text */
    --color-text-primary: #E6EDF3;
    --color-text-secondary: #8B949E;
    --color-text-tertiary: #6E7681;

    /* Accent */
    --color-accent: #0084FF;
    --color-accent-hover: #339DFF;

    /* Status */
    --color-status-blue: #4C9AFF;
    --color-status-green: #3FB950;
    --color-status-orange: #D29922;
    --color-status-red: #F85149;
    --color-status-purple: #A371F7;       /* Paused */
    --color-status-yellow: #E3B341;       /* Queued */

    /* Column colors */
    --color-column-backlog: #6E7681;
    --color-column-in-progress: #4C9AFF;
    --color-column-review: #D29922;
    --color-column-done: #3FB950;

    /* Typography */
    --font-sans: -apple-system, BlinkMacSystemFont, "Segoe UI", Helvetica, Arial, sans-serif;
    --font-mono: ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, monospace;

    /* Border radius */
    --radius-sm: 4px;
    --radius-md: 6px;
    --radius-lg: 8px;
}
```

---

## 7. Persistence

### 7.1 Database

- **File**: `data/kanban.db`
- **Engine**: SQLite via `modernc.org/sqlite` (pure Go, no CGO)
- **Connection**: Single connection opened at startup, shared across handlers
- **Schema**:

```sql
CREATE TABLE IF NOT EXISTS projects (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    path        TEXT NOT NULL UNIQUE,
    created_at  TEXT NOT NULL,
    updated_at  TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS tasks (
    id                           TEXT PRIMARY KEY,
    project_id                   TEXT NOT NULL,
    title                        TEXT NOT NULL,
    prompt                       TEXT NOT NULL,
    status                       TEXT NOT NULL DEFAULT 'backlog',
    column_order                 INTEGER NOT NULL DEFAULT 0,
    session_id                   TEXT,
    branch                       TEXT,
    output                       TEXT,
    status_state                 TEXT NOT NULL DEFAULT 'idle',
    llama_server_url             TEXT NOT NULL DEFAULT '',
    model_name                   TEXT NOT NULL DEFAULT '',
    max_consecutive_failed_turns INTEGER NOT NULL DEFAULT 3,
    auto_commit                  INTEGER NOT NULL DEFAULT 0,
    auto_pr                      INTEGER NOT NULL DEFAULT 0,
    plan_mode                    INTEGER NOT NULL DEFAULT 0,
    created_at                   TEXT NOT NULL,
    updated_at                   TEXT NOT NULL,
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS conversation_history (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    task_id    TEXT NOT NULL,
    turn_index INTEGER NOT NULL,
    role       TEXT NOT NULL,
    content    TEXT NOT NULL,
    tool_calls TEXT,          -- JSON array of tool call objects: [{"id": "...", "function": {"name": "...", "arguments": "..."}, "type": "function"}]
    tool_result TEXT,         -- JSON string of tool response fed back to the model
    created_at TEXT NOT NULL,
    FOREIGN KEY (task_id) REFERENCES tasks(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS config (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS task_dependencies (
    id             TEXT PRIMARY KEY,
    parent_task_id TEXT NOT NULL,
    child_task_id  TEXT NOT NULL,
    auto_start     INTEGER NOT NULL DEFAULT 1,
    created_at     TEXT NOT NULL,
    FOREIGN KEY (parent_task_id) REFERENCES tasks(id) ON DELETE CASCADE,
    FOREIGN KEY (child_task_id) REFERENCES tasks(id) ON DELETE CASCADE,
    UNIQUE(parent_task_id, child_task_id)
);

CREATE TABLE IF NOT EXISTS review_comments (
    id         TEXT PRIMARY KEY,
    task_id    TEXT NOT NULL,
    file_path  TEXT NOT NULL,
    line_num   INTEGER NOT NULL,
    comment    TEXT NOT NULL,
    resolved   INTEGER NOT NULL DEFAULT 0,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    FOREIGN KEY (task_id) REFERENCES tasks(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS home_agent_messages (
    id         TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    role       TEXT NOT NULL,
    content    TEXT NOT NULL,
    created_at TEXT NOT NULL,
    FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_tasks_project ON tasks(project_id);
CREATE INDEX IF NOT EXISTS idx_conversation_history_task ON conversation_history(task_id);
CREATE INDEX IF NOT EXISTS idx_task_dependencies_parent ON task_dependencies(parent_task_id);
CREATE INDEX IF NOT EXISTS idx_task_dependencies_child ON task_dependencies(child_task_id);
CREATE INDEX IF NOT EXISTS idx_review_comments_task ON review_comments(task_id);
CREATE INDEX IF NOT EXISTS idx_home_agent_messages_project ON home_agent_messages(project_id);
```

- **Write on**: Every state mutation (create, update, move, delete, reorder)
- **Defaults**: Configuration rows inserted on first startup if missing

### 7.2 Configuration Defaults

On startup, if the `config` table is empty, insert:

| key | value |
|-----|-------|
| `llama_server_url` | `http://localhost:8081/v1` |
| `model_name` | `` |
| `workspace_dir` | `./workspace` |
| `endpoint_limits` | `{"http://localhost:8081/v1":2}` |

### 7.3 Git Worktrees

- **Directory**: `./worktrees/` (relative to the Go server binary's working directory)
- **Per-task directory**: `./worktrees/<task-id>/` (flat structure, UUID v4 ensures uniqueness)
- **Source**: Created from a baseline capturing the current working tree state (including uncommitted changes). See Section 5.4 step 4 for the baseline mechanism.
- **Initial snapshot**: A commit is made immediately after worktree creation to capture the baseline (including uncommitted changes), enabling clean diff generation
- **Branch naming**: `task/<first-8-chars-of-uuid>`
- **Lifecycle**:
  - Created when task starts (POST /api/projects/:id/tasks/:taskId/start)
  - Preserved when task fails, is paused, is in Review, or is in Done — allowing post-completion inspection at any time
  - Cleaned up only when the task is explicitly deleted (not on column moves)
  - On delete: conversation_history rows are auto-deleted (CASCADE), worktree directory and branch are removed
  - The internal `__kanban_baseline_<task-id>` branch is deleted immediately after the worktree is created and the initial baseline commit is captured

---

## 8. Project Structure

```
kanban/
├── go.mod
├── go.sum
├── main.go                        // Entry point, starts HTTP server
├── backend/
│   ├── server.go                  // HTTP server setup, routing, middleware
│   ├── board.go                   // Board CRUD handlers (per-project)
│   ├── task.go                    // Task CRUD handlers (per-project)
│   ├── project.go                 // Project CRUD handlers
│   ├── stream.go                  // WebSocket streaming handler (fan-out broadcaster)
│   ├── execution.go               // Task execution orchestrator (multi-turn, tool calling)
│   ├── store.go                   // SQLite store
│   ├── llama.go                   // llama.cpp OpenAI-compatible client
│   ├── worktree.go                // Git worktree management
│   ├── config.go                  // Configuration handler (global)
│   └── types.go                   // Shared Go types
├── frontend/
│   ├── src/
│   │   ├── main.ts
│   │   ├── app.html
│   │   ├── app.css
│   │   └── lib/
│   │       ├── api.ts
│   │       ├── store.ts
│   │       ├── types.ts
│   │       └── components/
│   │           ├── App.svelte                    // Root layout with sidebar + board
│   │           ├── ProjectNavigation.svelte      // Collapsible sidebar project list
│   │           ├── ProjectSwitcher.svelte        // Top bar project name dropdown
│   │           ├── FileBrowser.svelte            // Directory browser for project selection
│   │           ├── Board.svelte
│   │           ├── Column.svelte
│   │           ├── Card.svelte
│   │           ├── CardDetail.svelte
│   │           ├── TaskStream.svelte
│   │           ├── StreamSection.svelte
│   │           ├── StreamToolCall.svelte
│   │           ├── StreamReasoning.svelte
│   │           ├── StreamText.svelte
│   │           ├── StreamActions.svelte
│   │           ├── CreateTaskModal.svelte
│   │           ├── TaskPrompt.svelte
│   │           ├── ConfigModal.svelte
│   │           └── TerminalPanel.svelte       // xterm.js-based terminal panel
│   ├── static/
│   ├── package.json
│   ├── svelte.config.js
│   ├── tsconfig.json
│   └── vite.config.ts
├── data/                          // Runtime data directory
│   └── kanban.db                  // SQLite database
├── worktrees/                     // Git worktrees (created at runtime)
├── .gitignore
└── SPECIFICATIONS.md
```

---

## 9. Backend Implementation Details

### 9.1 HTTP Server

- Uses `net/http` package for HTTP routing (no external router)
- WebSocket upgrade handling via `github.com/coder/websocket`
- Custom mux with path parameter extraction
- CORS middleware for frontend development
- JSON request/response encoding
- Server runs on configurable port (default: `3000`, overridable via `PORT` env var)
- Frontend served from `/` when built (static files from `frontend/dist/`)
- During frontend development, Vite dev server runs separately and proxies `/api` → `localhost:3000`

### 9.2 Routing

Simple regex-based router matching patterns like `/api/projects/{id}/tasks/{taskId}` and extracting path parameters into handler context.

### 9.3 Concurrency

- Each task execution runs in its own goroutine
- **Task queue dispatcher**: a background goroutine monitors per-endpoint concurrency limits from `config.EndpointLimits`. When tasks exceed the limit for their endpoint, they are queued (FIFO) and automatically dequeued when a slot becomes available.
- **WebSocket fan-out broadcaster**: single producer (task executor) broadcasts to multiple connected WebSocket clients
- Broadcaster map: `map[string]*broadcaster` where each broadcaster manages `map[*websocket.Conn]bool`
- **Ring buffer**: each broadcaster maintains a ring buffer of the last 100 stream events. When a new client subscribes, it receives the buffered events before receiving live events, enabling seamless reconnection after navigation or network interruptions.
- **Background continuation**: task execution continues running even when all WebSocket clients disconnect. The broadcaster preserves the ring buffer and the execution goroutine until the task completes, fails, or is explicitly stopped.
- **Broadcaster cleanup**: when a task reaches a terminal state (complete/failed/stopped), the broadcaster entry is removed from the map after broadcasting the final `complete` or `error` event to all connected clients. The ring buffer is discarded at this point — clients reconnecting after task completion will not receive a live stream but can view the final task output via the REST API.
- **Reconnect buffer-sync**: when a client reconnects to an active task stream, the broadcaster sends the last 100 buffered events (from the ring buffer) before switching to live delivery. The frontend detects a reconnect when the WebSocket is created within 5 seconds of a previous close for the same task, and automatically requests the buffer via a `{"type":"sync","seq":<last_seen_seq>}` message sent immediately after connection. The broadcaster responds with buffered events starting from `seq+1`.
- Task execution controlled with `context.Context` for cancellation
- **Board state mutex**: a per-project `sync.RWMutex` protects all board mutations (create, move, reorder, delete). Write locks are acquired for single-task mutations and bulk reorders. Read locks are acquired for board reads. This prevents race conditions during concurrent drag-drop reorders or dependency-triggered auto-starts. The mutex does NOT cover task execution state (`status_state` transitions), which are protected by the per-task execution goroutine's own context.

### 9.4 Error Handling

- Consistent JSON error responses: `{"error": "message"}`
- Appropriate HTTP status codes (400, 404, 409, 500)
- Graceful handling of llama.cpp connection failures

---

## 10. Frontend Implementation Details

### 10.1 Build Tooling

- **Vite** as the build tool and dev server
- **svelte-check** for type checking
- **svelte-preprocess** for TypeScript support

### 10.2 Vite Proxy Configuration

```typescript
// vite.config.ts
export default defineConfig({
    server: {
        port: 5173,
        proxy: {
            '/api': {
                target: 'http://localhost:3000',
                changeOrigin: true,
            },
        },
    },
});
```

### 10.3 API Client

All API functions are scoped to a project via the `projectId` parameter.

```typescript
// api.ts
const API_BASE = '/api';  // Vite proxies to localhost:3000 in dev

// -- Project endpoints --
export async function getProjects(): Promise<Project[]> { ... }
export async function createProject(path: string): Promise<Project> { ... }
export async function getProject(id: string): Promise<Project> { ... }
export async function updateProject(id: string, name: string): Promise<Project> { ... }
export async function deleteProject(id: string): Promise<void> { ... }

// -- Board endpoints (scoped to project) --
export async function getBoard(projectId: string): Promise<Board> { ... }
export async function reorderTasks(projectId: string, reorders: ReorderRequest[]): Promise<void> { ... }

// -- Task CRUD endpoints (scoped to project) --
export async function createTask(projectId: string, task: Partial<Task>): Promise<Task> { ... }
export async function getTask(projectId: string, taskId: string): Promise<Task> { ... }
export async function updateTask(projectId: string, taskId: string, task: Partial<Task>): Promise<Task> { ... }
export async function deleteTask(projectId: string, taskId: string): Promise<void> { ... }
export async function moveTask(projectId: string, taskId: string, status: string): Promise<Task> { ... }

// -- Task execution endpoints (scoped to project) --
export async function startTask(projectId: string, taskId: string): Promise<Task> { ... }
export async function stopTask(projectId: string, taskId: string): Promise<Task> { ... }
export async function retryTask(projectId: string, taskId: string): Promise<Task> { ... }
export function createTaskStream(projectId: string, taskId: string, onEvent: OnStreamEvent): WebSocket { ... }
export async function getTaskDiff(projectId: string, taskId: string): Promise<string> { ... }

// -- Configuration endpoints --
export async function getConfig(): Promise<Config> { ... }
export async function updateConfig(config: Config): Promise<Config> { ... }
```

### 10.4 WebSocket Stream Handler

The stream handler parses WebSocket messages into structured events that the TaskStream component uses to render agentic sections (text, reasoning, tool calls). This follows the same pattern as `deriveAgenticSections` in the llama.cpp UI.

```typescript
export type StreamEvent =
    | { type: 'token'; data: StreamTokenEvent }
    | { type: 'reasoning'; data: StreamReasoningEvent }
    | { type: 'reasoning_end'; data: StreamReasoningEndEvent }
    | { type: 'tool_call'; data: StreamToolCallEvent }
    | { type: 'tool_result'; data: StreamToolResultEvent }
    | { type: 'status'; data: StreamStatusEvent }
    | { type: 'queue'; data: StreamQueueEvent }
    | { type: 'complete'; data: StreamCompleteEvent }
    | { type: 'error'; data: StreamErrorEvent };

export type OnStreamEvent = (event: StreamEvent) => void;

export function createTaskStream(projectId: string, taskId: string, onEvent: OnStreamEvent): WebSocket {
    const ws = new WebSocket(`${API_BASE}/projects/${projectId}/tasks/${taskId}/stream`);
    ws.onmessage = (e) => {
        const msg = JSON.parse(e.data);
        const data = JSON.parse(msg.data);
        onEvent({ type: msg.type, data });
    };
    ws.onerror = (e) => onEvent({ type: 'error', data: { message: 'WebSocket error', code: 'ws_error' } });
    ws.onclose = () => onEvent({ type: 'status', data: { state: 'disconnected', activity: 'Stream closed' } });
    return ws;
}
```

### 10.5 Stream Section Model

The TaskStream component maintains an internal model of stream sections that maps to the agentic section types:

```typescript
export type StreamSectionType = 'text' | 'reasoning' | 'tool_call';

export interface StreamSection {
    id: string;
    type: StreamSectionType;
    content: string;          // Accumulated text/reasoning content
    isStreaming: boolean;     // True while content is still arriving
    toolCall?: {
        toolId: string;
        toolName: string;
        args: Record<string, unknown>;
        result?: string;
        success?: boolean;
        duration?: number;
    };
    reasoningSummary?: string; // Shown in collapsed state
    tokenCount?: number;       // Token count for reasoning sections
}
```

### 10.6 Auto-Scroll Hook

Inspired by `use-auto-scroll.svelte.ts` from the llama.cpp UI:

```typescript
// hooks/use-auto-scroll.svelte.ts
export function useAutoScroll(element: HTMLElement, enabled: boolean = true) {
    let atBottom = $state(true);
    let scrollTop = $state(0);
    let scrollHeight = $state(0);
    let clientHeight = $state(0);

    function checkAtBottom() {
        scrollTop = element.scrollTop;
        scrollHeight = element.scrollHeight;
        clientHeight = element.clientHeight;
        atBottom = scrollHeight - scrollTop - clientHeight < 32;
    }

    function scrollToBottom() {
        if (enabled && atBottom) {
            element.scrollTo({ top: element.scrollHeight, behavior: 'smooth' });
        }
    }

    return { atBottom, checkAtBottom, scrollToBottom };
}
```

---

## 11. Build and Run

### 11.1 Prerequisites

- Go 1.23+
- Node.js 18+ + npm
- llama.cpp compiled and running with `--server --host 0.0.0.0 --port 8081`
- Git

### 11.2 Backend

```bash
# Build
go build -o kanban-server .

# Run
./kanban-server
# Server starts on :3000 (or $PORT)
```

### 11.3 Frontend

```bash
# Install dependencies
cd frontend && npm install

# Development (hot reload, proxies /api to localhost:3000)
npm run dev
# Dev server on :5173

# Production build
npm run build
# Output: frontend/dist/ (served by Go backend)
```

### 11.4 Full Stack Development

```bash
# Terminal 1: llama.cpp server
./llama-server -m models/your-model.gguf --host 0.0.0.0 --port 8081

# Terminal 2: Go backend
go run .

# Terminal 3: Svelte frontend
cd frontend && npm run dev
```

---

## 12. Column Definitions

| Column | Status Value    | Color          | Description |
|--------|-----------------|----------------|-------------|
| Backlog | `backlog`     | Gray (#6E7681) | Tasks queued, not yet started |
| In Progress | `in_progress` | Blue (#4C9AFF) | AI is actively executing the task |
| Review | `review`      | Orange (#D29922) | Task completed, awaiting human review |
| Done | `done`        | Green (#3FB950) | Task approved and finalized |

### Allowed Transitions

| From | To |
|------|-----|
| Backlog | In Progress (→ queued or running), Done, (any column for reordering) |
| In Progress | Review (auto on completion), Backlog (manual), any column |
| Review | Done (accept — preserves worktree for inspection), In Progress (retry), Backlog |
| Done | Backlog (restore), any column, Delete (purges worktree + conversation history) |

### Status State → Column Mapping

| Status State | Column | Description |
|-------------|--------|-------------|
| `idle` | Backlog | Task created, not yet started |
| `queued` | In Progress | Waiting for endpoint slot, shows queue position |
| `running` | In Progress | AI actively executing |
| `paused` | In Progress | Connection lost, awaiting user resume |
| `awaiting_review` | Review | AI completed, awaiting human review |
| `failed` | In Progress | Execution failed, user can retry or manually move |
| `completed` | Done | Task finalized |

---

## 13. Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `3000` | Backend HTTP server port |
| `DATA_DIR` | `./data` | Directory for SQLite database (`kanban.db`) |
| `WORKTREES_DIR` | `./worktrees` | Directory for git worktrees |
| `FRONTEND_DIST` | `./frontend/dist` | Path to built frontend static files |

---

## 14. Execution Engine Boundary

The task execution engine (`backend/execution.go`) and the board state mutator (`backend/board.go`, `backend/task.go`) are separate concerns with a well-defined boundary:

- **Board state (`task.Status` / column)**: Only modified through the public HTTP API endpoints (`POST /move`, `PUT /tasks/:id`). The execution engine does NOT write to the board state directly.
- **Execution state (`task.StatusState`)**: Managed internally by the execution engine goroutine. The execution engine writes `status_state` transitions directly to the database (bypassing the HTTP API) since these are internal lifecycle markers.
- **Auto-move on completion**: When the execution engine determines a task should move columns (e.g., `awaiting_review` → auto-move to Review), it calls an internal `board.MoveTask(taskID, targetStatus)` function that acquires the per-project mutex and updates both `status` and `status_state` atomically. This is NOT the same as the HTTP `POST /move` endpoint, but uses the same underlying database mutation logic.
- **Status state transitions are NOT exposed as REST endpoints**: There is no `POST /set-status-state` endpoint. All `status_state` changes originate from either the execution engine goroutine or from the HTTP move/start/stop/retry/resume handlers which delegate to the execution engine.

This separation ensures that:
1. The board state is always consistent (protected by the per-project mutex).
2. The execution engine cannot corrupt board state through uncoordinated writes.
3. External API consumers can only trigger well-defined state transitions.

---

## 15. Home Agent WebSocket Protocol

The home agent WebSocket (`/api/projects/:id/home-agent/stream`) uses a distinct protocol from the task execution stream. Messages are JSON frames with a simpler envelope:

```go
// HomeAgentWSMessage - Envelope for home agent WebSocket frames
type HomeAgentWSMessage struct {
    Type string `json:"type"` // "user_message", "assistant_token", "assistant_done", "error"
    Data json.RawMessage `json:"data"`
}

// HomeAgentUserMessage - Client sends a message to the home agent
type HomeAgentUserMessage struct {
    Content string `json:"content"`
}

// HomeAgentTokenEvent - Streaming token from the home agent
type HomeAgentTokenEvent struct {
    Text string `json:"text"`
}

// HomeAgentDoneEvent - Home agent finished its response
type HomeAgentDoneEvent struct {
    MessageID string `json:"message_id"` // ID of the persisted HomeAgentMessage
    Tokens    struct {
        Prompt    int `json:"prompt"`
        Completed int `json:"completed"`
        Total     int `json:"total"`
    } `json:"tokens"`
}
```

**Protocol flow:**
1. Client connects to `/api/projects/:id/home-agent/stream` (one connection per project, not per task)
2. Client sends `{"type":"user_message","data":{"content":"..."}}` 
3. Server persists the user message to `home_agent_messages` table
4. Server streams assistant response as `assistant_token` events
5. Server sends `assistant_done` when the response is complete
6. The connection remains open for subsequent messages (persistent chat session)

The home agent does NOT use the task stream's `tool_call` / `tool_result` / `reasoning` event types. If the home agent needs to perform board actions (create tasks, link dependencies), these are executed server-side and broadcast as separate server-sent events on the same WebSocket connection.

---

## 16. Terminal Component

The terminal WebSocket endpoint (`/api/projects/:id/terminal`) provides an interactive shell session scoped to the project's git repository root. The frontend renders this via a **TerminalPanel.svelte** component:

**TerminalPanel.svelte**
- Full-height panel with dark background (`--color-surface-0`)
- Uses `xterm.js` for the terminal emulator (added as npm dependency)
- Connects to `/api/projects/:id/terminal` WebSocket
- All messages are JSON frames:
  - Client → Server: `{"type":"input","data":"<text>"}`, `{"type":"resize","rows":N,"cols":N}`
  - Server → Client: `{"type":"output","data":"<text>"}`, `{"type":"title","data":"<title>"}`
- Working directory set to the project root (NOT a task worktree)
- Session terminates when the WebSocket closes
- Accessible from the sidebar (terminal icon button) or via keyboard shortcut (Ctrl+`)

**API Client Addition:**
```typescript
// api.ts — Terminal
export function createTerminal(projectId: string, term: import('@xterm/xterm').Terminal): WebSocket {
    const ws = new WebSocket(`${API_BASE}/projects/${projectId}/terminal`);
    ws.onopen = () => {
        term.onData((data) => ws.send(JSON.stringify({ type: 'input', data })));
        term.onResize(({ cols, rows }) => ws.send(JSON.stringify({ type: 'resize', cols, rows })));
    };
    ws.onmessage = (e) => {
        const msg = JSON.parse(e.data);
        if (msg.type === 'output') term.write(msg.data);
        if (msg.type === 'title') term.title = msg.data;
    };
    return ws;
}
```

---

## 17. Svelte Version and Reactivity Model

The frontend uses **Svelte 5** with the Runes reactivity model. This is the version that introduces `$state()` and `$derived()` runes for fine-grained reactivity.

- Components use `$state()` for local reactive variables (as shown in the auto-scroll hook, section 10.6)
- Store-based state (`svelte/store` with `writable()`) is used for cross-component shared state (board, projects, config)
- The `$state()` rune is used for component-local reactive state
- The `$derived()` rune is used for computed values
- Both patterns coexist: stores for global state, runes for local component state

The `svelte-check` tool is configured for Svelte 5 compatibility. The `tsconfig.json` targets `ESNext` and enables `verbatimModuleSyntax`.

---

## 18. Baseline Branch Race Condition Prevention

Section 5.4 step 4 describes capturing uncommitted changes via a temporary baseline branch. To prevent race conditions when multiple tasks are started simultaneously on the same project:

- **Per-project worktree mutex**: A `sync.Mutex` (separate from the board state `RWMutex`) is acquired before the baseline creation sequence (stash → branch → apply → create worktree). This serializes worktree creation per project.
- **Timeout**: The mutex acquisition has a 30-second timeout. If the lock cannot be acquired within this window, the task start returns a `503 Service Unavailable` error with message "Another task is currently initializing its worktree. Please try again."
- **Baseline branch naming**: Uses `__kanban_baseline_<task-id>` (UUID-based, not timestamp-based) to ensure uniqueness even when multiple tasks are queued for the same project. The timestamp-based naming was replaced to avoid collisions under high concurrency.
- **Cleanup timing**: The `__kanban_baseline_<task-id>` branch is deleted immediately after the worktree is created and the initial baseline commit is captured. This occurs within the same mutex-protected critical section.

---

## 19. Config Storage Format

The `Config` struct (section 3.3) contains a nested `EndpointLimits map[string]int` field. The database `config` table stores flat key-value pairs. The mapping is:

| Config struct field | Database key | Storage format |
|---------------------|-------------|----------------|
| `LlamaServerURL` | `llama_server_url` | Plain string |
| `ModelName` | `model_name` | Plain string |
| `WorkspaceDir` | `workspace_dir` | Plain string (same as `WORKTREES_DIR` env var) |
| `EndpointLimits` | `endpoint_limits` | JSON-encoded map, e.g., `{"http://localhost:8081/v1":2}` |

The `ConfigStore` layer (`backend/config.go`) handles the serialization/deserialization:
- On read: `endpoint_limits` value is unmarshaled from JSON into `map[string]int`
- On write: `EndpointLimits` map is marshaled to JSON before storage
- The `GET /api/config` endpoint returns the fully assembled `Config` struct
- The `PUT /api/config` endpoint accepts the full `Config` struct and persists each field

---

## 20. Non-Goals (Out of Scope for V1)

- User authentication and multi-tenancy
- Model fine-tuning or prompt templates management
- File attachment support
- Notification system (email, Slack, etc.)
- Multi-agent orchestration beyond single-task tool calling
