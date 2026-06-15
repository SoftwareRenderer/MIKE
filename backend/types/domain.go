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
	ReasoningEffort         string    `json:"reasoning_effort"`
	MaxConsecutiveFailedTurns int     `json:"max_consecutive_failed_turns"`
	AutoCommit              bool      `json:"auto_commit"`
	AutoPR                  bool      `json:"auto_pr"`
	PlanMode                bool      `json:"plan_mode"`
	ParentTaskID            string    `json:"parent_task_id,omitempty"`
	TitleGenerating         bool      `json:"title_generating"`
	CurrentThreadID         string    `json:"current_thread_id,omitempty"`
	WorktreeExists          bool      `json:"worktree_exists"`
	CreatedAt               time.Time `json:"created_at"`
	UpdatedAt               time.Time `json:"updated_at"`
}

// Board represents the collection of tasks for a project.
type Board struct {
	Tasks []Task `json:"tasks"`
}

// TaskThread represents a conversation branch within a task.
type TaskThread struct {
	ID                    string     `json:"id"`
	TaskID                string     `json:"task_id"`
	ParentThreadID        string     `json:"parent_thread_id,omitempty"`
	Name                  string     `json:"name"`
	IsActive              bool       `json:"is_active"`
	GitBranch             string     `json:"git_branch,omitempty"`
	CheckpointMessageID   string     `json:"checkpoint_message_id,omitempty"`
	CheckpointMessageTime *time.Time `json:"checkpoint_message_time,omitempty"`
	CreatedAt             time.Time  `json:"created_at"`
}

// Config represents global server configuration.
type Config struct {
	WorkspaceDir          string     `json:"workspace_dir"`
	DatabasePath          string     `json:"database_path"`
	Providers             []Provider `json:"providers"`
	DefaultProvider       string     `json:"default_provider"`
	DefaultAutoCommit     *bool      `json:"default_auto_commit,omitempty"`
	DefaultPlanMode       *bool      `json:"default_plan_mode,omitempty"`
	DefaultMaxFailedTurns *int       `json:"default_max_failed_turns,omitempty"`
	DefaultSandboxImage   string     `json:"default_sandbox_image,omitempty"`
}

// ModelConfig represents a model with its per-model settings.
type ModelConfig struct {
	Name           string `json:"name"`
	ReasoningEffort string `json:"reasoning_effort"`
	ContextWindow  *int   `json:"context_window,omitempty"` // nil if not reported by LLM
}

// Provider is a named LLM server preset with a list of available models.
type Provider struct {
	Name           string        `json:"name"`
	DisplayName    string        `json:"display_name"`
	URL            string        `json:"url"`
	Models         []ModelConfig `json:"models"`
	Concurrency    int           `json:"concurrency"`
	DefaultModel   string        `json:"default_model"`
	InputPrice     float64       `json:"input_price"`      // USD per 1M input (non-cached) tokens
	OutputPrice    float64       `json:"output_price"`     // USD per 1M output tokens
	CacheReadPrice float64       `json:"cache_read_price"` // USD per 1M cache-read tokens
	CacheWritePrice float64      `json:"cache_write_price"` // USD per 1M cache-write tokens (overrides input_price when set)
}

// Project represents a workspace project.
type Project struct {
	ID             string    `json:"id"`
	Name           string    `json:"name"`
	Path           string    `json:"path"`
	OriginBranch   string    `json:"origin_branch,omitempty"`
	SandboxEnabled bool      `json:"sandbox_enabled"`
	CreatedAt      time.Time `json:"created_at"`
	UpdatedAt      time.Time `json:"updated_at"`
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
	ThreadID         string    `json:"thread_id,omitempty"`
	Role             string    `json:"role"`
	Content          string    `json:"content"`
	ReasoningContent string    `json:"reasoning_content,omitempty"`
	ToolCalls        string    `json:"tool_calls,omitempty"`
	ToolResult       string    `json:"tool_result,omitempty"`
	ModelName        string    `json:"model_name,omitempty"`
	ProviderName     string    `json:"provider_name,omitempty"`
	PromptTokens     int       `json:"prompt_tokens,omitempty"`
	PredictedTokens  int       `json:"predicted_tokens,omitempty"`
	CacheTokens      int       `json:"cache_tokens,omitempty"`
	PromptMs         int       `json:"prompt_ms,omitempty"`
	PredictedMs      int       `json:"predicted_ms,omitempty"`
	Duration         int64     `json:"duration,omitempty"`
	CreatedAt        time.Time `json:"created_at"`
}

// ProjectStats holds aggregated token usage for a project.
type ProjectStats struct {
	TotalTasks         int        `json:"total_tasks"`
	TotalPromptTokens  int        `json:"total_prompt_tokens"`
	TotalCacheTokens   int        `json:"total_cache_tokens"`
	TotalProcessedTokens int      `json:"total_processed_tokens"`
	TotalGeneratedTokens int      `json:"total_generated_tokens"`
	AvgPromptTps       float64    `json:"avg_prompt_tps"`
	AvgGenerationTps   float64    `json:"avg_generation_tps"`
	ByModel            []ModelStats `json:"by_model"`
	TotalCost          float64    `json:"total_cost"` // USD total cost across all models
}

// ModelStats holds token usage aggregated by provider/model.
type ModelStats struct {
	ModelName        string  `json:"model_name"`
	ProviderName     string  `json:"provider_name,omitempty"`
	PromptTokens     int     `json:"prompt_tokens"`
	CacheTokens      int     `json:"cache_tokens"`
	ProcessedTokens  int     `json:"processed_tokens"`
	GeneratedTokens  int     `json:"generated_tokens"`
	AvgPromptTps     float64 `json:"avg_prompt_tps"`
	AvgGenerationTps float64 `json:"avg_generation_tps"`
	TaskCount        int     `json:"task_count"`
	Cost             float64 `json:"cost"` // USD cost for this model's usage
}
