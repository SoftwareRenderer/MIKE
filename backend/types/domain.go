package types

import "time"

// Task represents a card on the Kanban board.
type Task struct {
	ID                      string    `json:"id"`
	ProjectID               string    `json:"project_id"`
	Title                   string    `json:"title"`
	Prompt                  string    `json:"prompt"`
	Status                  string    `json:"status"`
	ColumnOrder             int       `json:"column_order"`
	SessionID               string    `json:"session_id"`
	Branch                  string    `json:"branch"`
	Output                  string    `json:"output"`
	StatusState             string    `json:"status_state"`
	LlamaServerURL          string    `json:"llama_server_url"`
	ModelName               string    `json:"model_name"`
	MaxConsecutiveFailedTurns int     `json:"max_consecutive_failed_turns"`
	AutoCommit              bool      `json:"auto_commit"`
	AutoPR                  bool      `json:"auto_pr"`
	PlanMode                bool      `json:"plan_mode"`
	CreatedAt               time.Time `json:"created_at"`
	UpdatedAt               time.Time `json:"updated_at"`
}

// Board represents the collection of tasks for a project.
type Board struct {
	Tasks []Task `json:"tasks"`
}

// Config represents global server configuration.
type Config struct {
	LlamaServerURL        string     `json:"llama_server_url"`
	ModelName             string     `json:"model_name"`
	WorkspaceDir          string     `json:"workspace_dir"`
	Providers             []Provider `json:"providers"`
	DefaultProvider       string     `json:"default_provider"`
	DefaultAutoCommit     *bool      `json:"default_auto_commit,omitempty"`
	DefaultPlanMode       *bool      `json:"default_plan_mode,omitempty"`
	DefaultMaxFailedTurns *int       `json:"default_max_failed_turns,omitempty"`
	CardModelBadge        string     `json:"card_model_badge"`
}

// ModelConfig represents a model with its per-model settings.
type ModelConfig struct {
	Name            string `json:"name"`
	ReasoningEffort string `json:"reasoning_effort"`
}

// Provider is a named LLM server preset with a list of available models.
type Provider struct {
	Name         string        `json:"name"`
	DisplayName  string        `json:"display_name"`
	URL          string        `json:"url"`
	Models       []ModelConfig `json:"models"`
	Concurrency  int           `json:"concurrency"`
	DefaultModel string        `json:"default_model"`
}

// Project represents a workspace project.
type Project struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Path      string    `json:"path"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ProjectCounts holds task counts per status for a project.
type ProjectCounts struct {
	Backlog    int `json:"backlog"`
	InProgress int `json:"in_progress"`
	Review     int `json:"review"`
}

// TaskDependency represents a parent-child link between tasks.
type TaskDependency struct {
	ID           string    `json:"id"`
	ParentTaskID string    `json:"parent_task_id"`
	ChildTaskID  string    `json:"child_task_id"`
	AutoStart    bool      `json:"auto_start"`
	CreatedAt    time.Time `json:"created_at"`
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

// HomeAgentMessage represents a message in the home agent conversation.
type HomeAgentMessage struct {
	ID        string    `json:"id"`
	ProjectID string    `json:"project_id"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
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
	Name      string `json:"name"`
	Hash      string `json:"hash"`
	IsCurrent bool   `json:"is_current"`
}

// GitStatus represents the working tree status.
type GitStatus struct {
	Branch    string   `json:"branch"`
	Hash      string   `json:"hash"`
	Clean     bool     `json:"clean"`
	Added     []string `json:"added"`
	Modified  []string `json:"modified"`
	Deleted   []string `json:"deleted"`
	Untracked []string `json:"untracked"`
}

// TaskMessage represents a chat message for a task.
type TaskMessage struct {
	ID               string    `json:"id"`
	TaskID           string    `json:"task_id"`
	Role             string    `json:"role"`
	Content          string    `json:"content"`
	ReasoningContent string    `json:"reasoning_content,omitempty"`
	ToolCalls        string    `json:"tool_calls,omitempty"`
	ToolResult       string    `json:"tool_result,omitempty"`
	ModelName        string    `json:"model_name,omitempty"`
	PromptTokens     int       `json:"prompt_tokens,omitempty"`
	PredictedTokens  int       `json:"predicted_tokens,omitempty"`
	PromptMs         int       `json:"prompt_ms,omitempty"`
	PredictedMs      int       `json:"predicted_ms,omitempty"`
	Duration         int64     `json:"duration,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
}
