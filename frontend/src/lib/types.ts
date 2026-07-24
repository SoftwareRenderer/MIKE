// TypeScript interfaces matching backend Go types

/** Maps raw tool names to their UI display names (fallback for old persisted messages). */
const TOOL_DISPLAY_NAMES: Record<string, string> = {
    read_file: 'Read',
    write_file: 'Write',
    edit_file: 'Edit',
    bash: 'Execute',
    ask_question: 'Question',
    request_review: 'Done',
};

/** Returns the display name for a tool, falling back to the raw name. */
export function getToolDisplayName(displayName: string | undefined, toolName: string): string {
    if (displayName) return displayName;
    return TOOL_DISPLAY_NAMES[toolName] || toolName;
}

export interface PortForward {
    container_port: number;
}

export interface SandboxMount {
    host_path: string;
    container_path: string;
    mode: 'rw' | 'ro';
}

export interface SandboxEnv {
    key: string;
    value: string;
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
    sandbox_mounts?: SandboxMount[];
    sandbox_envs?: SandboxEnv[];
    sandbox_capabilities?: string[];
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
    base_commit?: string; // commit hash of base_ref at task start
    branch_tip?: string; // commit hash at time of commit-and-done
    output?: string;
    execution_state: string;
    has_merge_conflict: boolean;
    llama_server_url?: string;
    model_name?: string;
    reasoning_effort?: string;
    max_consecutive_failed_turns: number;
    auto_commit: boolean;
    auto_merge: boolean;
    subtasks_auto_merge: boolean;
    operating_mode: string; // "act", "plan", or "ideate"
    parent_task_id?: string;
    title_generating: boolean;
    current_thread_id?: string;
    created_at: string;
    updated_at: string;
    assigned_ports?: AssignedPort[];
    base_ref?: string;
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

export interface ChatEntry {
    id: string;
    role: string;
    content: string;
    mergeLabel?: string;
    isError?: boolean;
    reasoningContent?: string;
    hasReasoning?: boolean;
    isStreaming?: boolean;
    threadId?: string;
    attachments?: Array<{ mimeType: string; data: string }>;
    toolCalls?: Array<{
        toolId: string;
        toolName: string;
        displayName?: string;
        args: Record<string, unknown>;
        raw_args?: string;
        sandbox?: boolean;
        path?: string;
        result?: string;
        resultTruncated?: boolean;
        originalLines?: number;
        originalBytes?: number;
        messageId?: string;
        success?: boolean;
        duration?: number;
        isPending?: boolean;
        bytesWritten?: number;
        linesAdded?: number;
        linesRemoved?: number;
        startedAt?: number;
    }>;
    toolCall?: {
        toolName: string;
        displayName?: string;
        args: Record<string, unknown>;
        raw_args?: string;
        result?: string;
        success?: boolean;
        duration?: number;
        isPending?: boolean;
        startedAt?: number;
    };
    error?: {
        code: string;
        message: string;
    };
    stats?: {
        model_name: string;
        prompt_tokens: number;
        predicted_tokens: number;
        prompt_ms: number;
        predicted_ms: number;
        duration: number;
    };
    createdAt?: string;
    queued?: boolean;
}

export type DiffData = {
    files?: Record<string, {
        hash: string;
        type: string;
        added?: number;
        removed?: number;
        truncated?: boolean;
        origAdded?: number;
        origRemoved?: number;
        line_count?: number;
        file_size?: number;
        is_binary?: boolean;
        staged?: boolean;
    }>;
    short_stat?: string;
};

export interface Board {
    tasks: Task[];
}

export interface Config {
    workspace_dir: string;
    database_path: string;
    version: string;
    providers: Provider[];
    default_provider: string;
    default_auto_commit?: boolean;
    default_auto_merge?: boolean;
    default_subtasks_auto_merge?: boolean;
    default_operating_mode?: string; // "act", "plan", or "ideate"
    default_max_failed_turns?: number;
    encryption_enabled?: boolean;
    registry_url?: string;
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
    power_consumption_w?: number;        // Watts
    electricity_cost_per_kwh?: number;   // USD per kWh
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
    power_kwh?: number; // kWh
    task_count: number;
    cost: number; // USD (API + electricity)
}

export interface ProjectStats {
    total_tasks: number;
    total_prompt_tokens: number;
    total_cache_tokens: number;
    total_processed_tokens: number;
    total_generated_tokens: number;
    avg_prompt_tps: number;
    avg_generation_tps: number;
    total_power_kwh?: number; // kWh
    by_model: ModelStats[];
    total_cost: number; // USD (API + electricity)
}

// Stream events
export type StreamEventType =
    | 'token'
    | 'reasoning'
    | 'reasoning_end'
    | 'tool_call'
    | 'tool_result'
    | 'tool_progress'
    | 'tool_output'
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
    | 'port_conflict'
    | 'prompt_progress'
    | 'hint';

export interface StreamEvent {
    type: StreamEventType;
    data: unknown;
}

// ─── Diff / Commit / Task Event types ───

export type DiffStatsEntry = {
    hash: string;
    files: number;
    additions: number;
    deletions: number;
    untracked_n?: number;
    file_hashes?: Record<string, string>;
    removed_files?: string[];
    staged_paths?: string[];
    has_committed_changes?: boolean;
    already_merged?: boolean;
};

export type DiffEntry = {
    hash: string;
    diff?: string;
    untracked?: Array<{
        path: string;
        diff: string;
        file_size?: number;
        line_count?: number;
    }>;
    files?: Record<string, {
        hash: string;
        type: string;
        added?: number;
        removed?: number;
        line_count?: number;
        file_size?: number;
    }>;
    updated_at: number;
};

export type CommitQueueEntry = { taskID: string; status: 'queued' | 'processing' };

export type TaskEventData = { task_id: string; type: string; data: any };

// ─── Extension types ───

export type ExtensionType = 'lsp' | 'ui';

export interface ExtensionManifest {
    display_name: string;
    description: string;
    author: string;
    homepage?: string;
    license_url?: string;
    deprecated: boolean;
    config_schema?: ConfigSchema;
    lsp?: LSPConfig;
    ui?: UIExtensionConfig;
}

export interface LSPConfig {
    command: string;
    args: string[];
    extensions: string[];
    language: string;
    initParamsOverride?: Record<string, any>;
}

export interface UIExtensionConfig {
    module_url: string;
    injection_points: string[];
}

export interface ConfigSchema {
    type: string;
    properties?: Record<string, SchemaProperty>;
    required?: string[];
}

export interface SchemaProperty {
    type: string;
    title?: string;
    description?: string;
    default?: any;
    minimum?: number;
    maximum?: number;
    enum?: any[];
}

export interface ContainerConfig {
    image?: string;
    dockerfile?: string;
    entrypoint?: string[];
    working_dir?: string;
}

export interface ExtensionSandboxConfig {
    network_isolation: boolean;
    memory_limit: number;
    forward_ports?: PortForward[];
    mounts?: SandboxMount[];
    envs?: SandboxEnv[];
    capabilities?: string[];
}

export interface Extension {
    id: string;
    name: string;
    version: string;
    type: ExtensionType;
    manifest: ExtensionManifest;
    config: Record<string, any>;
    container_config: ContainerConfig;
    sandbox_config: ExtensionSandboxConfig;
    install_source: string;
    image_ref?: string;
    enabled: boolean;
    running: boolean;
    ports?: AssignedPort[];
    installed_at: string;
    built_at: string;
}

export interface RegistryEntry {
    name: string;
    version: string;
    type: ExtensionType;
    display_name: string;
    description: string;
    author: string;
    homepage?: string;
    license_url?: string;
    image?: string;
    dockerfile_url?: string;
    manifest_url?: string;
}

export interface ExtensionUpdate {
    id: string;
    name: string;
    current_version: string;
    available_version: string;
}

