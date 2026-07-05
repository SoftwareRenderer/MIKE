// TypeScript interfaces matching backend Go types

/** Maps raw tool names to their UI display names (fallback for old persisted messages). */
export const TOOL_DISPLAY_NAMES: Record<string, string> = {
    read_file: 'Read',
    write_file: 'Write',
    edit_file: 'Edit',
    bash: 'Execute',
    task_complete: 'Done',
};

/** Returns the display name for a tool, falling back to the raw name. */
export function getToolDisplayName(displayName: string | undefined, toolName: string): string {
    if (displayName) return displayName;
    return TOOL_DISPLAY_NAMES[toolName] || toolName;
}

export interface PortForward {
    container_port: number;
}

export interface AssignedPort {
    host_port: number;
    container_port: number;
}

export interface Project {
    id: string;
    name: string;
    path: string;
    origin_branch?: string;
    sandbox_enabled?: boolean;
    sandbox_network_isolation?: boolean;
    sandbox_image?: string;
    sandbox_dockerfile_path?: string;
    sandbox_memory_limit?: number;
    forward_ports?: PortForward[];
    active_terminal_ports?: AssignedPort[];
    created_at: string;
    updated_at: string;
}

export interface Task {
    id: string;
    project_id: string;
    title: string;
    prompt: string;
    status: string;
    column_order: number;
    session_id?: string;
    branch?: string;
    output?: string;
    execution_state: string;
    llama_server_url?: string;
    model_name?: string;
    reasoning_effort?: string;
    max_consecutive_failed_turns: number;
    auto_commit: boolean;
    auto_merge: boolean;
    subtasks_auto_merge: boolean;
    plan_mode: boolean;
    parent_task_id?: string;
    title_generating: boolean;
    current_thread_id?: string;
    created_at: string;
    updated_at: string;
    assigned_ports?: AssignedPort[];
}

export interface Thread {
    id: string;
    task_id: string;
    parent_thread_id?: string;
    name: string;
    is_active: boolean;
    git_branch?: string;
    checkpoint_message_id?: string;
    checkpoint_message_time?: string;
    checkpoint_commit_hash?: string;
    created_at: string;
}

export interface Board {
    tasks: Task[];
}

export interface Config {
    workspace_dir: string;
    database_path: string;
    providers: Provider[];
    default_provider: string;
    default_auto_commit?: boolean;
    default_auto_merge?: boolean;
    default_subtasks_auto_merge?: boolean;
    default_plan_mode?: boolean;
    default_max_failed_turns?: number;
    encryption_enabled?: boolean;
}

export interface ProviderModel {
    name: string;
    reasoning_effort?: 'off' | 'low' | 'medium' | 'high' | 'max';
    context_window?: number; // nil if not reported by LLM
}

/** Reasoning effort options for the settings UI. */
export const REASONING_EFFORT_OPTIONS: Array<{ value: string; label: string; tokens: string }> = [
    { value: 'off', label: 'Off', tokens: 'Disabled' },
    { value: 'low', label: 'Low', tokens: '512 tokens' },
    { value: 'medium', label: 'Medium', tokens: '2,048 tokens' },
    { value: 'high', label: 'High', tokens: '8,192 tokens' },
    { value: 'max', label: 'Max', tokens: 'Unlimited' },
];

export interface Provider {
    name: string;
    display_name: string;
    kind?: 'llm' | 'agent';    // "llm" (default) or "agent"
    url: string;
    api_key?: string;          // masked ("••••••••••") when read from config, full value when sent
    models: ProviderModel[];
    concurrency: number;
    default_model?: string;
    input_price?: number;      // USD per 1M input (non-cached) tokens
    output_price?: number;     // USD per 1M output tokens
    cache_read_price?: number; // USD per 1M cache-read tokens
    cache_write_price?: number; // USD per 1M cache-write tokens (overrides input_price when set)
}

/** Sentinel URL used when a task uses Pi agent instead of an LLM provider. */
export const PI_SENTINEL_URL = 'pi://agent';

/** Check if a provider is a Pi agent. */
export function isAgentProvider(p: Provider): boolean {
    return p.kind === 'agent';
}

export interface TaskMessage {
    id: string;
    task_id: string;
    thread_id?: string;
    role: string;
    content: string;
    reasoning_content?: string;
    has_reasoning?: boolean; // true if message has reasoning (even if content is suppressed)
    tool_calls?: string;
    tool_result?: string;
    attachments?: string | Array<{ mimeType: string; data: string }>;
    model_name?: string;
    prompt_tokens?: number;
    predicted_tokens?: number;
    prompt_ms?: number;
    predicted_ms?: number;
    duration?: number;
    git_commit_hash?: string;
    created_at: string;
}

export interface GitCommit {
    hash: string;
    short: string;
    subject: string;
    body?: string; // full commit message body (excluding subject)
    author: string;
    date: string;
    parents?: string[];
    branches?: string[]; // branch names pointing at this commit
}

export interface GitLogResponse {
    commits: GitCommit[];
    current_branch: string;
    origin_tip?: string; // commit hash where origin/<branch> points
    has_more?: boolean; // true if more commits available beyond this batch
}

/** A commit from a task's worktree history. */
export interface TaskCommit {
    hash: string;
    short: string;
    subject: string;
    date: string;
}

export interface MoveTaskRequest {
    status: string;
}

export interface ReorderRequest {
    id: string;
    column_order: number;
}

export interface ProjectCounts {
    backlog: number;
    in_progress: number;
    review: number;
}

export interface ModelStats {
    model_name: string;
    provider_name: string;
    prompt_tokens: number;
    cache_tokens: number;
    processed_tokens: number;
    generated_tokens: number;
    avg_prompt_tps: number;
    avg_generation_tps: number;
    task_count: number;
    cost: number; // USD cost for this model's usage
}

export interface ProjectStats {
    total_tasks: number;
    total_prompt_tokens: number;
    total_cache_tokens: number;
    total_processed_tokens: number;
    total_generated_tokens: number;
    avg_prompt_tps: number;
    avg_generation_tps: number;
    by_model: ModelStats[];
    total_cost: number; // USD total cost across all models
}

// Stream events
export type StreamEventType =
    | 'token'
    | 'reasoning'
    | 'reasoning_end'
    | 'tool_call'
    | 'tool_result'
    | 'tool_progress'
    | 'status'
    | 'queue'
    | 'complete'
    | 'error'
    | 'user_message'
    | 'stats'
    | 'thread_switch'
    | 'summarization'
    | 'git_checkpoint_pending'
    | 'git_checkpoint_complete'
    | 'merge_resolution'
    | 'diff_changed'
    | 'build_progress'
    | 'port_conflict';

export interface StreamEvent {
    type: StreamEventType;
    data: unknown;
}

export type StreamSectionType = 'text' | 'reasoning' | 'tool_call';

export interface StreamSection {
    id: string;
    type: StreamSectionType;
    content: string;
    isStreaming: boolean;
    toolCall?: {
        toolId: string;
        toolName: string;
        displayName?: string;
        args: Record<string, unknown>;
        sandbox?: boolean;
        result?: string;
        success?: boolean;
        duration?: number;
    };
    reasoningSummary?: string;
    tokenCount?: number;
}