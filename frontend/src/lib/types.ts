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
    operating_mode: string; // "act", "plan", or "explore"
    parent_task_id?: string;
    title_generating: boolean;
    summarizing: boolean;
    current_thread_id?: string;
    worktree_exists?: boolean;
    created_at: string;
    updated_at: string;
    assigned_ports?: AssignedPort[];
    base_ref?: string;
    session_cost?: number;
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
    severity?: string;
    mergeLabel?: string;
    isError?: boolean;
    reasoningContent?: string;
    hasReasoning?: boolean;
    isStreaming?: boolean;
    threadId?: string;
    buildLog?: {
        title: string;
        lines: string[];
        done?: boolean;
    };
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
    worker_provider: string;
    worker_model: string;
    utility_provider: string;
    utility_model: string;
    default_auto_commit?: boolean;
    default_auto_merge?: boolean;
    default_subtasks_auto_merge?: boolean;
    default_operating_mode?: string; // "act", "plan", or "explore"
    default_max_failed_turns?: number;
    default_auto_summarize_threshold?: number; // percent of context window, 1-100
    encryption_enabled?: boolean;
    registry_url?: string;
    keyboard_shortcuts?: KeyboardShortcuts;
}

/**
 * Keyboard shortcut bindings (server-side). Normalized form: modifiers in
 * fixed order (control, meta, shift, alt) joined with "+" then the key.
 * undefined = default, "" = disabled, non-empty = custom.
 */
export interface KeyboardShortcuts {
    create_task?: string;
    create_task_submit?: string;
    /** Opens the terminal panel on pages with a terminal button. */
    open_terminal?: string;
    /** Starts the task in the task view while the Start Task button is shown. */
    start_task?: string;
    /** Hide the keycap hints on shortcut buttons (tooltips stay). */
    hide_hints?: boolean;
}

export interface ProviderModel {
    name: string;
    reasoning_effort?: 'off' | 'low' | 'medium' | 'high' | 'max';
    context_window?: number; // nil if not reported by LLM
    input_price?: number;
    output_price?: number;
    cache_read_price?: number;
    cache_write_price?: number;
    power_consumption_w?: number;
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
    id?: number;               // stable identifier for LLM providers; URL stays editable
    name?: string;             // agent provider name (from manifest); empty for LLM providers
    display_name?: string;    // only populated for agent providers (from manifest); LLM display names are derived from URL
    kind?: 'llm' | 'agent';    // "llm" (default) or "agent"
    url: string;
    api_key?: string;          // masked ("••••••••••") when read from config, full value when sent
    models: ProviderModel[];
    concurrency: number;
    max_requests_per_second: number; // 0 = unlimited; max new LLM request starts per second
    agent_config_dir?: string; // optional host path override for agent config dir; empty means use extension default
    input_price?: number;      // USD per 1M input (non-cached) tokens
    output_price?: number;     // USD per 1M output tokens
    cache_read_price?: number; // USD per 1M cache-read tokens
    cache_write_price?: number; // USD per 1M cache-write tokens (overrides input_price when set)
    power_consumption_w?: number;        // Watts
    electricity_cost_per_kwh?: number;   // USD per kWh
}

/** URL prefix for agent providers (e.g., 'agent://pi'). */
export const AGENT_URL_PREFIX = 'agent://';

/** Check if a provider is an agent (non-LLM kind). Accepts any provider-shaped object. */
export function isAgentProvider(p: { kind?: string; url: string }): boolean {
    return p.kind === 'agent' || p.url.startsWith(AGENT_URL_PREFIX);
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

export type ExtensionType = 'lsp' | 'ui' | 'css' | 'agent';

export interface ExtensionManifest {
    display_name: string;
    description: string;
    author: string;
    homepage?: string;
    license_url?: string;
    version: string;
    priority?: number; // loading order: lower values load first (default 0)
    config_schema?: ConfigSchema;
    capabilities?: ExtensionCapabilities;
}

export interface ExtensionCapabilities {
    lsp?: LSPConfig;
    ui?: UIExtensionConfig;
    css?: CSSExtensionConfig;
    agent?: AgentExtensionConfig;
    proxy?: ProxyExtensionConfig;
}

export interface ProxyExtensionConfig {
    allowed_domains: string[];
    headers?: Record<string, string>;
}

export interface AgentExtensionConfig {
    wasm_module: string;
    capabilities?: string[];
    description?: string;    // short description shown in provider card
    setup_info?: string;     // setup instructions shown in provider card
    config_schema?: ConfigSchema;
    sandbox_mount?: AgentConfigMount; // default config dir mount into sandbox containers
}

export interface AgentConfigMount {
    host_path: string;            // host path to agent config dir
    container_path: string;        // mount point inside the container
    mode: 'rw' | 'ro';             // "rw" (default) or "ro"
    envs?: SandboxEnv[];          // env key-value pairs for non-sandbox use
}

export interface LSPConfig {
    command: string;
    args: string[];
    extensions: string[];
    language: string;
    initParamsOverride?: Record<string, any>;
    editor_enabled?: boolean;  // LSP features in editor (diagnostics, completions, etc.)
    tool_calls_enabled?: boolean;  // LSP tools for agent (references, definition, etc.)
}

export interface UIExtensionConfig {
    injection_points: string[];
    /** Fully-qualified capability names the extension requests (e.g. "createTask.description.insertText"). */
    permissions?: string[];
}

export interface CSSExtensionConfig {
    css_url: string; // global stylesheet URL (relative to server root)
}

export interface ConfigSchema {
    properties?: SchemaProperty[]; // array to preserve insertion order
}

export interface SchemaProperty {
    key: string;
    type: string; // "string", "number", "boolean", "mount", "env"
    title?: string;
    description?: string;
    default?: any;
    minimum?: number;
    maximum?: number;
    enum?: any[];
    sensitive?: boolean;
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
    author?: string;
    version: string;
    type: ExtensionType;
    manifest: ExtensionManifest;
    config: Record<string, any>;
    container_config: ContainerConfig;
    sandbox_config: ExtensionSandboxConfig;
    install_source: string;
    image_ref?: string;
    enabled: boolean;
    /** Absolute filesystem path of the extension (only for local installs). */
    local_path?: string;
    running: boolean;
    ports?: AssignedPort[];
    installed_at: string;
    built_at: string;
    repo?: string;
    commit?: string;
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
    repo?: string;
    commit?: string;
    subdir?: string;
    capabilities?: Record<string, any>;
    /** Absolute on-disk directory for filesystem (unpublished) local extensions. */
    local_path?: string;
}

export interface ExtensionUpdate {
    id: string;
    name: string;
    current_version: string;
    available_version: string;
    current_commit?: string;
    available_commit?: string;
    /** UI permissions this update would newly grant (present in the registry version but not installed). */
    new_permissions?: string[];
}

