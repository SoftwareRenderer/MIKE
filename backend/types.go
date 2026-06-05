package backend

import (
	"encoding/json"
	"time"
)

// Task represents a card on the Kanban board.
type Task struct {
	ID                       string    `json:"id"`
	ProjectID                string    `json:"project_id"`
	Title                    string    `json:"title"`
	Prompt                   string    `json:"prompt"`
	Status                   string    `json:"status"`
	ColumnOrder              int       `json:"column_order"`
	SessionID                string    `json:"session_id"`
	Branch                   string    `json:"branch"`
	Output                   string    `json:"output"`
	StatusState              string    `json:"status_state"`
	LlamaServerURL           string    `json:"llama_server_url"`
	ModelName                string    `json:"model_name"`
	MaxConsecutiveFailedTurns int      `json:"max_consecutive_failed_turns"`
	AutoCommit               bool      `json:"auto_commit"`
	AutoPR                   bool      `json:"auto_pr"`
	PlanMode                 bool      `json:"plan_mode"`
	CreatedAt                time.Time `json:"created_at"`
	UpdatedAt                time.Time `json:"updated_at"`
}

// Board represents the collection of tasks for a project.
type Board struct {
	Tasks []Task `json:"tasks"`
}

// Config represents global server configuration.
type Config struct {
	LlamaServerURL string         `json:"llama_server_url"`
	ModelName      string         `json:"model_name"`
	WorkspaceDir   string         `json:"workspace_dir"`
	EndpointLimits map[string]int `json:"endpoint_limits"`
	Providers      []Provider     `json:"providers"`          // named provider presets
	DefaultProvider string        `json:"default_provider"`   // name of default provider
}

// Provider is a named LLM server preset with a list of available models.
type Provider struct {
	Name        string   `json:"name"`         // internal identifier (e.g., "neptune")
	DisplayName string   `json:"display_name"` // human-readable label (e.g., "Neptune")
	URL         string   `json:"url"`          // server URL (e.g., "http://neptune:8010/v1")
	Models      []string `json:"models"`       // available model names
}

// Project represents a workspace project.
type Project struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// MoveTaskRequest is the request body for moving a task.
type MoveTaskRequest struct {
	Status string `json:"status"`
}

// ReorderRequest is the request body for reordering a task.
type ReorderRequest struct {
	ID          string `json:"id"`
	ColumnOrder int    `json:"column_order"`
}

// CreateProjectRequest is the request body for creating a project.
type CreateProjectRequest struct {
	Path string `json:"path"`
	Name string `json:"name,omitempty"`
}

// UpdateProjectRequest is the request body for updating a project.
type UpdateProjectRequest struct {
	Name string `json:"name"`
}

// TaskDependency represents a parent-child link between tasks.
type TaskDependency struct {
	ID           string    `json:"id"`
	ParentTaskID string    `json:"parent_task_id"`
	ChildTaskID  string    `json:"child_task_id"`
	AutoStart    bool      `json:"auto_start"`
	CreatedAt    time.Time `json:"created_at"`
}

// CreateDependencyRequest is the request body for creating a dependency.
type CreateDependencyRequest struct {
	ChildTaskID string `json:"child_task_id"`
	AutoStart   bool   `json:"auto_start"`
}

// ReviewComment represents a line-level review comment.
type ReviewComment struct {
	ID        string    `json:"id"`
	TaskID    string    `json:"task_id"`
	FilePath  string    `json:"file_path"`
	LineNum   int       `json:"line_num"`
	Comment   string    `json:"comment"`
	Resolved  bool      `json:"resolved"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// CreateReviewCommentRequest is the request body for creating a review comment.
type CreateReviewCommentRequest struct {
	FilePath string `json:"file_path"`
	LineNum  int    `json:"line_num"`
	Comment  string `json:"comment"`
}

// UpdateReviewCommentRequest is the request body for updating a review comment.
type UpdateReviewCommentRequest struct {
	Comment  *string `json:"comment"`
	Resolved *bool   `json:"resolved"`
}

// HomeAgentMessage represents a message in the home agent conversation.
type HomeAgentMessage struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

// CreateHomeAgentMessageRequest is the request body for sending a home agent message.
type CreateHomeAgentMessageRequest struct {
	Content string `json:"content"`
}

// ConversationHistory represents a persisted message from a task execution.
type ConversationHistory struct {
	ID         int       `json:"id"`
	TaskID     string    `json:"task_id"`
	TurnIndex  int       `json:"turn_index"`
	Role       string    `json:"role"`
	Content    string    `json:"content"`
	ToolCalls  string    `json:"tool_calls"`
	ToolResult string    `json:"tool_result"`
	CreatedAt  time.Time `json:"created_at"`
}

// GitCommit represents a git commit in the log.
type GitCommit struct {
	Hash    string `json:"hash"`
	Short   string `json:"short"`
	Subject string `json:"subject"`
	Author  string `json:"author"`
	Date    string `json:"date"`
}

// GitBranch represents a git branch.
type GitBranch struct {
	Name     string `json:"name"`
	Hash     string `json:"hash"`
	IsCurrent bool  `json:"is_current"`
}

// GitStatus represents the working tree status.
type GitStatus struct {
	Branch   string   `json:"branch"`
	Hash     string   `json:"hash"`
	Clean    bool     `json:"clean"`
	Added    []string `json:"added"`
	Modified []string `json:"modified"`
	Deleted  []string `json:"deleted"`
	Untracked []string `json:"untracked"`
}

// GitLogRequest is the request body for git log.
type GitLogRequest struct {
	Branch string `json:"branch,omitempty"`
	Limit  int    `json:"limit,omitempty"`
}

// GitDiffRequest is the request body for git diff.
type GitDiffRequest struct {
	Ref string `json:"ref"`
	Path string `json:"path,omitempty"`
}

// GitCheckoutRequest is the request body for git checkout.
type GitCheckoutRequest struct {
	Branch string `json:"branch"`
}

// GitPullRequest is the request body for git pull.
type GitPullRequest struct {
	Branch string `json:"branch,omitempty"`
}

// GitPushRequest is the request body for git push.
type GitPushRequest struct {
	Branch string `json:"branch,omitempty"`
}

// -- WebSocket message types --

// WSMessage is the envelope for all WebSocket frames.
type WSMessage struct {
	Type string `json:"type"`
	Data json.RawMessage `json:"data"`
}

// StreamTokenEvent is an individual text token from the AI.
type StreamTokenEvent struct {
	Text string `json:"text"`
}

// StreamReasoningEvent is reasoning/thinking content.
type StreamReasoningEvent struct {
	Text string `json:"text"`
}

// StreamReasoningEndEvent marks the end of a reasoning block.
type StreamReasoningEndEvent struct {
	Summary string `json:"summary"`
}

// StreamToolCallEvent is an AI tool invocation request.
type StreamToolCallEvent struct {
	ToolID   string                 `json:"tool_id"`
	ToolName string                 `json:"tool_name"`
	Args     map[string]interface{} `json:"args"`
}

// StreamToolResultEvent is the result of a tool invocation.
type StreamToolResultEvent struct {
	ToolID   string `json:"tool_id"`
	Success  bool   `json:"success"`
	Output   string `json:"output"`
	Duration int64  `json:"duration"`
}

// StreamStatusEvent is a task status/activity update.
type StreamStatusEvent struct {
	State    string `json:"state"`
	Activity string `json:"activity"`
}

// StreamQueueEvent is a queue position update.
type StreamQueueEvent struct {
	Position        int `json:"position"`
	EstimatedWait   int `json:"estimated_wait_seconds"`
}

// StreamCompleteEvent is sent when task execution finishes.
type StreamCompleteEvent struct {
	State   string `json:"state"`
	Summary string `json:"summary"`
	Tokens  struct {
		Prompt    int `json:"prompt"`
		Completed int `json:"completed"`
		Total     int `json:"total"`
	} `json:"tokens"`
	Duration int64 `json:"duration"`
}

// StreamErrorEvent is sent on errors during execution.
type StreamErrorEvent struct {
	Message string `json:"message"`
	Code    string `json:"code"`
}

// -- Task Message types --

// TaskMessage represents a chat message for a task.
type TaskMessage struct {
	ID              string    `json:"id"`
	TaskID          string    `json:"task_id"`
	Role            string    `json:"role"`
	Content         string    `json:"content"`
	ToolCalls       string    `json:"tool_calls,omitempty"`
	ToolResult      string    `json:"tool_result,omitempty"`
	ModelName       string    `json:"model_name,omitempty"`
	PromptTokens    int       `json:"prompt_tokens,omitempty"`
	PredictedTokens int       `json:"predicted_tokens,omitempty"`
	PromptMs        int       `json:"prompt_ms,omitempty"`
	PredictedMs     int       `json:"predicted_ms,omitempty"`
	Duration        int64     `json:"duration,omitempty"`
	CreatedAt       time.Time `json:"created_at"`
}

// -- Home Agent WebSocket types --

// HomeAgentWSMessage is the envelope for home agent WebSocket frames.
type HomeAgentWSMessage struct {
	Type string `json:"type"`
	Data json.RawMessage `json:"data"`
}

// HomeAgentUserMessage is sent by the client to the home agent.
type HomeAgentUserMessage struct {
	Content string `json:"content"`
}

// HomeAgentTokenEvent is a streaming token from the home agent.
type HomeAgentTokenEvent struct {
	Text string `json:"text"`
}

// HomeAgentDoneEvent marks the end of a home agent response.
type HomeAgentDoneEvent struct {
	MessageID string `json:"message_id"`
	Tokens    struct {
		Prompt    int `json:"prompt"`
		Completed int `json:"completed"`
		Total     int `json:"total"`
	} `json:"tokens"`
}

// -- Terminal WebSocket types --

// TerminalMessage is the envelope for terminal WebSocket frames.
type TerminalMessage struct {
	Type string `json:"type"`
	Data string `json:"data"`
	Rows int    `json:"rows,omitempty"`
	Cols int    `json:"cols,omitempty"`
}

// -- Internal types --

// ToolCall represents a parsed tool call from the model.
type ToolCall struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Arguments string `json:"arguments"`
}

// ToolResult represents the result of executing a tool.
type ToolResult struct {
	Success bool   `json:"success"`
	Output  string `json:"output"`
}