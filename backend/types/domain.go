package types

import (
	"time"
)

// Task represents a card on the Kanban board.
type Task struct {
	ID                        string `json:"id"`
	ProjectID                 string `json:"project_id"`
	Title                     string `json:"title"`
	Prompt                    string `json:"prompt"`
	Status                    string `json:"status"`
	ColumnOrder               int    `json:"column_order"`
	SessionID                 string `json:"session_id"`
	Branch                    string `json:"branch"`
	BaseRef                   string `json:"base_ref,omitempty"`
	BaseCommit                string `json:"base_commit,omitempty"` // commit hash of base_ref at task start
	BranchTip                 string `json:"branch_tip,omitempty"`  // commit hash at time of commit-and-done
	Output                    string `json:"output"`
	ExecutionState            string `json:"execution_state"`
	HasMergeConflict          bool   `json:"has_merge_conflict"`
	LlamaServerURL            string `json:"llama_server_url"`
	ModelName                 string `json:"model_name"`
	ReasoningEffort           string `json:"reasoning_effort"`
	MaxConsecutiveFailedTurns int    `json:"max_consecutive_failed_turns"`
	AutoCommit                bool   `json:"auto_commit"`
	AutoMerge                 bool   `json:"auto_merge"`
	SubtasksAutoMerge         bool   `json:"subtasks_auto_merge"` // parent-only: children inherit this as auto_merge
	OperatingMode             string `json:"operating_mode"`      // "act" (default), "plan", or "explore"
	ParentTaskID              string `json:"parent_task_id,omitempty"`
	TitleGenerating           bool   `json:"title_generating"`
	Summarizing               bool   `json:"summarizing"`
	CurrentThreadID           string `json:"current_thread_id,omitempty"`
	WorktreeExists            bool   `json:"worktree_exists"`
	// Autostarted records whether this execution was started autonomously
	Autostarted   bool           `json:"-"`
	CreatedAt     time.Time      `json:"created_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
	AssignedPorts []AssignedPort `json:"assigned_ports,omitempty"`
	SessionCost   float64        `json:"session_cost"`
}

// IsPlanMode returns true if the task is in plan mode.
func (t *Task) IsPlanMode() bool {
	return t.OperatingMode == "plan"
}

// IsExploreMode returns true if the task is in exploration mode.
func (t *Task) IsExploreMode() bool {
	return t.OperatingMode == "explore"
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
	CheckpointCommitHash  string     `json:"checkpoint_commit_hash,omitempty"` // git commit at fork point
	WorktreeStashHash     string     `json:"worktree_stash_hash,omitempty"`    // git stash create hash for worktree restoration
	CreatedAt             time.Time  `json:"created_at"`
}

// Config represents global server configuration.
type Config struct {
	WorkspaceDir                  string             `json:"workspace_dir"`
	DatabasePath                  string             `json:"database_path"`
	RegistryURL                   string             `json:"registry_url"`
	Providers                     []Provider         `json:"providers"`
	WorkerProvider                string             `json:"worker_provider"`
	WorkerModel                   string             `json:"worker_model"`
	UtilityProvider               string             `json:"utility_provider"`
	UtilityModel                  string             `json:"utility_model"`
	DefaultAutoCommit             *bool              `json:"default_auto_commit"`
	DefaultAutoMerge              *bool              `json:"default_auto_merge"`
	DefaultSubtasksAutoMerge      *bool              `json:"default_subtasks_auto_merge"`
	DefaultOperatingMode          *string            `json:"default_operating_mode"`
	DefaultMaxFailedTurns         *int               `json:"default_max_failed_turns,omitempty"`
	DefaultAutoSummarizeThreshold *int               `json:"default_auto_summarize_threshold,omitempty"`
	KeyboardShortcuts             *KeyboardShortcuts `json:"keyboard_shortcuts,omitempty"`
}

// KeyboardShortcuts holds user-customizable keyboard shortcut bindings.
// A nil binding means "use the default"; "" means disabled. Values are
// normalized: modifiers in fixed order (control, meta, shift, alt) joined
// with "+" then the key (e.g. "c", "shift+k").
type KeyboardShortcuts struct {
	// CreateTask opens the create-task dialog on the project board.
	CreateTask *string `json:"create_task,omitempty"`
	// CreateTaskSubmit creates the task inside the create-task dialog.
	CreateTaskSubmit *string `json:"create_task_submit,omitempty"`
	// OpenTerminal opens the terminal panel on pages with a terminal button.
	OpenTerminal *string `json:"open_terminal,omitempty"`
	// HideHints hides the keycap hints on shortcut buttons (tooltips stay).
	HideHints *bool `json:"hide_hints,omitempty"`
}

// ModelConfig represents a model with its per-model settings.
type ModelConfig struct {
	Name              string   `json:"name"`
	ReasoningEffort   string   `json:"reasoning_effort"`
	ContextWindow     *int     `json:"context_window,omitempty"`      // nil if not reported by LLM
	InputPrice        *float64 `json:"input_price,omitempty"`         // USD per 1M input (non-cached) tokens; nil = inherit provider default
	OutputPrice       *float64 `json:"output_price,omitempty"`        // USD per 1M output tokens; nil = inherit provider default
	CacheReadPrice    *float64 `json:"cache_read_price,omitempty"`    // USD per 1M cache-read tokens; nil = inherit provider default
	CacheWritePrice   *float64 `json:"cache_write_price,omitempty"`   // USD per 1M cache-write tokens; nil = inherit provider default
	PowerConsumptionW *float64 `json:"power_consumption_w,omitempty"` // Watts override; nil = inherit provider default
}

// ProviderKind distinguishes between standard LLM providers and coding agents.
const (
	ProviderKindLLM   = "llm"
	ProviderKindAgent = "agent"
)

// AgentSandbox describes mounts and env vars for an agent sandbox container.
type AgentSandbox struct {
	Mounts []SandboxMount `json:"mount"` // bind mounts into the container
	Envs   []SandboxEnv   `json:"env"`   // env vars for the agent process
}

// Provider is a named LLM server preset with a list of available models.
type Provider struct {
	ID                    int           `json:"id"`   // stable identifier for LLM providers (agents use Name); URL stays editable
	Name                  string        `json:"name"` // agent provider name (manifest-derived); empty for LLM providers
	DisplayName           string        `json:"display_name"`
	Kind                  string        `json:"kind"` // "llm" (default) or "agent"
	URL                   string        `json:"url"`
	APIKey                *string       `json:"api_key,omitempty"` // nil=preserve, ""=clear, value=set; encrypted in DB, masked in API responses
	Models                []ModelConfig `json:"models"`
	Concurrency           int           `json:"concurrency"`
	MaxRequestsPerSecond  int           `json:"max_requests_per_second"`     // 0 = unlimited; max new LLM request starts per second
	AgentConfigDir        string        `json:"agent_config_dir,omitempty"`  // optional host path override for the agent config dir; empty means use extension default
	AgentDescription      string        `json:"agent_description,omitempty"` // agent description text (from manifest); empty for LLM providers
	AgentSetupInfo        string        `json:"agent_setup_info,omitempty"`  // agent setup instructions (from manifest); empty for LLM providers
	InputPrice            float64       `json:"input_price"`                 // USD per 1M input (non-cached) tokens
	OutputPrice           float64       `json:"output_price"`                // USD per 1M output tokens
	CacheReadPrice        float64       `json:"cache_read_price"`            // USD per 1M cache-read tokens
	CacheWritePrice       float64       `json:"cache_write_price"`           // USD per 1M cache-write tokens (overrides input_price when set)
	PowerConsumptionW     float64       `json:"power_consumption_w"`         // Watts
	ElectricityCostPerKwh float64       `json:"electricity_cost_per_kwh"`    // USD per kWh
}

// IsAgent returns true if this provider is any coding agent (not a standard LLM).
// Agents are identified by having a non-LLM kind.
func (p *Provider) IsAgent() bool {
	return p.EffectiveKind() != ProviderKindLLM
}

// EffectiveKind returns the provider kind, defaulting to "llm" for backward compatibility.
func (p *Provider) EffectiveKind() string {
	if p.Kind == "" {
		return ProviderKindLLM
	}
	return p.Kind
}

// APIKeyValue returns the API key as a string (empty if nil).
func (p *Provider) APIKeyValue() string {
	if p.APIKey == nil {
		return ""
	}
	return *p.APIKey
}

// PortForward specifies a container port to forward. Host port is auto-assigned at runtime.
type PortForward struct {
	ContainerPort int `json:"container_port"`
}

// AssignedPort is a runtime host→container port mapping assigned during sandbox creation.
type AssignedPort struct {
	HostPort      int `json:"host_port"`
	ContainerPort int `json:"container_port"`
}

// SandboxMount specifies a custom host path bind mount for sandbox containers.
type SandboxMount struct {
	HostPath      string `json:"host_path"`
	ContainerPath string `json:"container_path"`
	Mode          string `json:"mode"` // "rw" or "ro"
}

// SandboxEnv specifies an environment variable to set in sandbox containers.
type SandboxEnv struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}

// Project represents a workspace project.
type Project struct {
	ID                      string         `json:"id"`
	Name                    string         `json:"name"`
	Path                    string         `json:"path"`
	OriginBranch            string         `json:"origin_branch,omitempty"`
	SandboxEnabled          bool           `json:"sandbox_enabled"`
	SandboxNetworkIsolation bool           `json:"sandbox_network_isolation"`
	SandboxImage            string         `json:"sandbox_image,omitempty"`
	SandboxDockerfilePath   string         `json:"sandbox_dockerfile_path,omitempty"`
	SandboxMemoryLimit      int            `json:"sandbox_memory_limit"`
	ForwardPorts            []PortForward  `json:"forward_ports,omitempty"`
	SandboxMounts           []SandboxMount `json:"sandbox_mounts,omitempty"`
	SandboxEnvs             []SandboxEnv   `json:"sandbox_envs,omitempty"`
	SandboxCapabilities     []string       `json:"sandbox_capabilities,omitempty"`
	ActiveTerminalPorts     []AssignedPort `json:"active_terminal_ports,omitempty"` // transient: assigned ports from project-level terminal
	CreatedAt               time.Time      `json:"created_at"`
	UpdatedAt               time.Time      `json:"updated_at"`
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

// GitCommit represents a git commit in the log.
type GitCommit struct {
	Hash     string   `json:"hash"`
	Short    string   `json:"short"`
	Subject  string   `json:"subject"`
	Body     string   `json:"body,omitempty"` // full commit message body (excluding subject)
	Author   string   `json:"author"`
	Date     string   `json:"date"`
	Parents  []string `json:"parents,omitempty"`
	Branches []string `json:"branches,omitempty"` // branch names pointing at this commit
}

// GitLogResponse wraps the commit log with branch/origin metadata.
type GitLogResponse struct {
	Commits       []GitCommit `json:"commits"`
	CurrentBranch string      `json:"current_branch"`       // name of the checked-out branch
	OriginTip     string      `json:"origin_tip,omitempty"` // commit hash where origin/<branch> points
	HasMore       bool        `json:"has_more,omitempty"`   // true if more commits available beyond this batch
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
	Severity         string    `json:"severity,omitempty"` // system-message severity: "" (default/warning), "info", "error"
	Content          string    `json:"content"`
	ReasoningContent string    `json:"reasoning_content,omitempty"`
	HasReasoning     bool      `json:"has_reasoning,omitempty"` // true if message has reasoning (even if suppressed)
	ToolCalls        string    `json:"tool_calls,omitempty"`
	ToolResult       string    `json:"tool_result,omitempty"`
	ResultTruncated  bool      `json:"result_truncated,omitempty"` // true if tool_result output was truncated
	Attachments      string    `json:"attachments,omitempty"`      // JSON array of {mimeType, data} for images
	ModelName        string    `json:"model_name,omitempty"`
	ProviderName     string    `json:"provider_name,omitempty"`
	PromptTokens     int       `json:"prompt_tokens,omitempty"`
	PredictedTokens  int       `json:"predicted_tokens,omitempty"`
	CacheTokens      int       `json:"cache_tokens,omitempty"`
	PromptMs         int       `json:"prompt_ms,omitempty"`
	PredictedMs      int       `json:"predicted_ms,omitempty"`
	Duration         int64     `json:"duration,omitempty"`
	GitCommitHash    string    `json:"git_commit_hash,omitempty"`
	IsLoopHint       bool      `json:"is_loop_hint,omitempty"` // true for injected loop-detection hints (shown in UI, excluded from LLM context)
	CreatedAt        time.Time `json:"created_at"`
}

// ProjectStats holds aggregated token usage for a project.
type ProjectStats struct {
	TotalTasks                     int          `json:"total_tasks"`
	TotalPromptTokens              int          `json:"total_prompt_tokens"`
	TotalCacheTokens               int          `json:"total_cache_tokens"`
	TotalProcessedTokens           int          `json:"total_processed_tokens"`
	TotalGeneratedTokens           int          `json:"total_generated_tokens"`
	TotalProcessedTokensWithTiming int          `json:"total_processed_tokens_with_timing"`
	TotalGeneratedTokensWithTiming int          `json:"total_generated_tokens_with_timing"`
	AvgPromptTps                   float64      `json:"avg_prompt_tps"`
	AvgGenerationTps               float64      `json:"avg_generation_tps"`
	TotalPowerKwh                  float64      `json:"total_power_kwh"` // kWh
	ByModel                        []ModelStats `json:"by_model"`
	TotalCost                      float64      `json:"total_cost"` // USD (API + electricity)
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
	PredictedMs      int64   `json:"predicted_ms"` // Cumulative generation time in milliseconds
	PowerKwh         float64 `json:"power_kwh"`    // kWh
	TaskCount        int     `json:"task_count"`
	Cost             float64 `json:"cost"` // USD (API + electricity)
}
