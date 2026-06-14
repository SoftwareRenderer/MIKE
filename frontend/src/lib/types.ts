// TypeScript interfaces matching backend Go types

/** Maps raw tool names to their UI display names (fallback for old persisted messages). */
export const TOOL_DISPLAY_NAMES: Record<string, string> = {
    read_file: 'Read',
    write_file: 'Write',
    execute_command: 'Execute',
    task_complete: 'Done',
};

/** Returns the display name for a tool, falling back to the raw name. */
export function getToolDisplayName(displayName: string | undefined, toolName: string): string {
    if (displayName) return displayName;
    return TOOL_DISPLAY_NAMES[toolName] || toolName;
}

export interface Project {
    id: string;
    name: string;
    path: string;
    origin_branch?: string;
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
    status_state: string;
    llama_server_url?: string;
    model_name?: string;
    reasoning_effort?: string;
    max_consecutive_failed_turns: number;
    auto_commit: boolean;
    auto_pr: boolean;
    plan_mode: boolean;
    parent_task_id?: string;
    title_generating: boolean;
    current_thread_id?: string;
    created_at: string;
    updated_at: string;
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
    default_plan_mode?: boolean;
    default_max_failed_turns?: number;
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
    url: string;
    models: ProviderModel[];
    concurrency: number;
    default_model?: string;
    input_price?: number;      // USD per 1M input (non-cached) tokens
    output_price?: number;     // USD per 1M output tokens
    cache_read_price?: number; // USD per 1M cache-read tokens
    cache_write_price?: number; // USD per 1M cache-write tokens (overrides input_price when set)
}

export interface TaskDependency {
    id: string;
    parent_task_id: string;
    child_task_id: string;
    auto_start: boolean;
    created_at: string;
}

export interface ReviewComment {
    id: string;
    task_id: string;
    file_path: string;
    line_num: number;
    comment: string;
    resolved: boolean;
    created_at: string;
    updated_at: string;
}

export interface HomeAgentMessage {
    id: string;
    project_id: string;
    role: string;
    content: string;
    created_at: string;
}

export interface TaskMessage {
    id: string;
    task_id: string;
    thread_id?: string;
    role: string;
    content: string;
    reasoning_content?: string;
    tool_calls?: string;
    tool_result?: string;
    model_name?: string;
    prompt_tokens?: number;
    predicted_tokens?: number;
    prompt_ms?: number;
    predicted_ms?: number;
    duration?: number;
    created_at: string;
}

export interface GitCommit {
    hash: string;
    short: string;
    subject: string;
    author: string;
    date: string;
}

export interface GitBranch {
    name: string;
    hash: string;
    is_current: boolean;
}

export interface GitStatus {
    branch: string;
    hash: string;
    clean: boolean;
    added: string[];
    modified: string[];
    deleted: string[];
    untracked: string[];
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
    | 'status'
    | 'queue'
    | 'complete'
    | 'error'
    | 'user_message'
    | 'stats'
    | 'thread_switch'
    | 'compression';

export interface StreamEvent {
    type: StreamEventType;
    data: unknown;
}

export interface StreamTokenEvent {
    text: string;
}

export interface StreamReasoningEvent {
    text: string;
}

export interface StreamReasoningEndEvent {
    summary: string;
}

export interface StreamToolCallEvent {
    tool_id: string;
    tool_name: string;
    tool_display_name?: string;
    args: Record<string, unknown>;
}

export interface StreamToolResultEvent {
    tool_id: string;
    success: boolean;
    output: string;
    duration: number;
}

export interface StreamStatusEvent {
    state: string;
    activity: string;
}

export interface StreamQueueEvent {
    position: number;
    estimated_wait_seconds?: number;
}

export interface StreamCompleteEvent {
    state: string;
    summary: string;
    model_name: string;
    prompt_tokens: number;
    predicted_tokens: number;
    prompt_ms: number;
    predicted_ms: number;
    duration: number;
}

export interface StreamErrorEvent {
    message: string;
    code?: string;
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
        result?: string;
        success?: boolean;
        duration?: number;
    };
    reasoningSummary?: string;
    tokenCount?: number;
}