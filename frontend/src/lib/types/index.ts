// TypeScript interfaces matching backend Go types

import { t, type MessageKey } from '$lib/i18n';

/** Raw tool name → the catalog key naming it (the fallback for old persisted messages).
 *  Keys, not translated strings: a `t()` evaluated here would freeze whatever locale the
 *  module happened to load in, and a language switch must re-render a tool badge. */
const TOOL_DISPLAY_KEYS: Record<string, MessageKey> = {
    read_file: 'tool.read',
    write_file: 'tool.write',
    edit_file: 'tool.edit',
    bash: 'tool.execute',
    ask_question: 'tool.question',
    request_review: 'tool.done',
};

/** Returns the display name for a tool, falling back to the raw name. */
export function getToolDisplayName(displayName: string | undefined, toolName: string): string {
    if (displayName) return displayName;
    const key = TOOL_DISPLAY_KEYS[toolName];
    return key ? t(key) : toolName;
}

export interface PortForward {
    container_port: number;
    /** Host address to publish on; empty means every interface. */
    host_ip?: string;
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
    /** Bound address; empty means every interface. */
    host_ip?: string;
}

// Live memory usage of a task's sandbox container, pushed as the
// `sandbox_stats` task event. Values are whole MB; 0 means unknown and
// limit_mb 0 means no enforced limit.
export interface SandboxMemoryStats {
    running: boolean;
    memory_current_mb: number;
    memory_limit_mb: number;
    memory_peak_mb: number;
    oom_kills: number;
}

export interface Project {
    id: string;
    name: string;
    path: string;
    // Client-only flags for the in-flight "clone from git" placeholder row:
    // `cloning` marks the optimistic sidebar entry (never persisted), and
    // `cloneProgress` carries the 0-100 clone percentage streamed from the
    // server while it runs.
    cloning?: boolean;
    cloneProgress?: number;
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
    /** The project's answer to the "Assisted-by" commit trailer; default on. */
    commit_assisted_by?: boolean;
    active_terminal_ports?: AssignedPort[];
    created_at: string;
    updated_at: string;
}

/** What `PUT /api/projects/{id}` takes — a partial update, so an absent field is left alone —
 * mirroring `UpdateProjectRequest` in `backend/types/request.go`. */
export interface ProjectUpdates {
    name?: string;
    sandbox_enabled?: boolean;
    sandbox_network_isolation?: boolean;
    sandbox_image?: string;
    sandbox_dockerfile_path?: string;
    sandbox_memory_limit?: number;
    sandbox_forward_ports?: PortForward[];
    sandbox_mounts?: SandboxMount[];
    sandbox_envs?: SandboxEnv[];
    sandbox_capabilities?: string[];
    commit_assisted_by?: boolean;
}

/** A dismissed memory alert and the numbers it showed, as the task row carries them. */
export interface MemoryAlertAckPayload {
    peak_mb: number;
    oom_kills: number;
    limit_mb: number;
}

export interface Task {
    id: string;
    project_id: string;
    title: string;
    prompt: string;
    status: string;
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
    /** When the task last changed column — the board's sort key (newest move first). */
    moved_at: string;
    assigned_ports?: AssignedPort[];
    base_ref?: string;
    session_cost?: number;
    /** Peak container memory (MB) of the last sandbox run; absent when no run recorded one. */
    sandbox_memory_peak_mb?: number;
    /** Processes the OOM killer took from that same container; absent when none were recorded. */
    sandbox_oom_kills?: number;
    /** The alert dismissed and its numbers; null when nobody dismissed one. */
    memory_alert_ack?: MemoryAlertAckPayload | null;
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

/**
 * One tool call as the client holds it.
 *
 * The fields are the `tool_call` / `tool_result` frames' payload (`backend/types/ws.go`) under the names the
 * client uses; `contentIndex` and `reasoningIndex` are not on the wire - the stream handler stamps them with
 * the offsets the call arrived at, which is what lets `chat-layout` interleave the card inside the reasoning
 * text.
 */
export interface ToolCallInfo {
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
    interrupted?: boolean; // stopped by a user message, not a failure
    duration?: number;
    isPending?: boolean;
    bytesWritten?: number;
    linesAdded?: number;
    linesRemoved?: number;
    startedAt?: number;
    contentIndex?: number;
    reasoningIndex?: number;
}

export interface ChatEntry {
    id: string;
    role: string;
    content: string;
    severity?: string;
    /** Sub-classification within a role — advisory rows: 'diagnostics' | 'merge_conflict' | 'auto_answer'. */
    kind?: string;
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
    toolCalls?: ToolCallInfo[];
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
    /** The queue action this outgoing entry came from, so a re-derive does not draw it twice. */
    actionId?: string;
    /** This browser wrote the entry, so dismissing it needs no request: the server has no such row. */
    local?: boolean;
}

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
    hide_model_badge?: boolean; // hide the model badge on task cards
    // May MIKE ask the release manifest whether a newer version exists? Absent means never asked - off, and
    // still owed the first-run question, which MIKE_DISABLE_UPDATES outranks.
    update_check_enabled?: boolean;
    run_siblings_parallel?: boolean; // start all of a parent's subtasks at once (default off)
    sort_oldest_first?: boolean; // put the least recently moved card on top of a column (default off)
    auto_maximize_chat_mobile?: boolean; // maximize the chat while its input has focus (default on)
    reclaim_idle_sandbox?: boolean; // release a waiting task's container to the memory budget (default off)
    encryption_enabled?: boolean;
    tls_ca_available?: boolean; // server runs its own cert signed by its local CA → GET /api/tls/ca offers it
    // Everything MIKE started may share this ceiling, in MB; memory_total_mb is the installed RAM it
    // was computed from and memory_budget_pct the configured percentage. Absent (or 0) when the gate is
    // off, which is the case a config written by such a server leaves behind.
    memory_budget_mb?: number;
    memory_total_mb?: number;
    memory_budget_pct?: number;
    keyboard_shortcuts?: KeyboardShortcuts;
/** The extensions whose contributed tools reach the network from the server process. Read-only: a PUT
     *  naming this key writes nothing. */
    server_network_extensions?: NetworkedExtension[];
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
    /** Maximizes or minimizes the focused area (chat, diff, editor, terminal). */
    toggle_fullscreen?: string;
    /**
     * Modifier+Click that toggles task-card multi-select (e.g. "meta+click").
     * Unbound modifier clicks reach the browser, so they open the card's link.
     */
    multi_select_click?: string;
    /** Hide the keycap hints on shortcut buttons (tooltips stay). */
    hide_hints?: boolean;
}

export interface ProviderModel {
    name: string;
    reasoning_effort?: string; // known level or a custom value passed through verbatim to the server
    context_window?: number; // nil if not reported by LLM
    input_price?: number;
    output_price?: number;
    cache_read_price?: number;
    cache_write_price?: number;
    power_consumption_w?: number;
}

/**
 * The reasoning effort levels offered as suggestions; any other value is passed to the server as given.
 *
 * A function, not a const array: its labels come from the locale, so a module-scope `t()` answer would
 * outlive every language switch.
 */
export function reasoningEffortOptions(): Array<{ value: string; label: string }> {
    return [
        { value: 'default', label: t('effort.default') },
        { value: 'none', label: t('effort.none') },
        { value: 'low', label: t('effort.low') },
        { value: 'medium', label: t('effort.medium') },
        { value: 'high', label: t('effort.high') },
    ];
}

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
    stream_idle_seconds: number; // 0 = no ceiling; seconds a response may stay silent before it is ended
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

/**
 * One prompt of a conversation, as the turn read answers it: enough to name the prompt and to find its row
 * again. `text` is the head of the prompt, bounded by the server; its first line is what a header shows.
 */
export interface ChatTurn {
    id: string;
    created_at: string;
    text: string;
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
    /** Sub-classification within a role — advisory rows: 'diagnostics' | 'merge_conflict' | 'auto_answer'. */
    kind?: string;
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

// A wait a click is watching, named by its domain. Rendered by QueueIndicator. `phase` arrives only
// from the provider wait, which reads differently before the first token and after it.
export interface LockWait {
    domain: 'worktree' | 'stash' | string;
    seconds: number;
    phase?: string;
}

// Stream events
export type StreamEventType =
    | 'token'
    | 'reasoning'
    | 'tool_call'
    | 'tool_result'
    | 'tool_progress'
    | 'tool_output'
    | 'status'
    | 'queue'
    | 'lock_wait'
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
    | 'sandbox_stats'
    | 'prompt_progress'
    | 'hint'
    | 'advisory';

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

export type CommitQueueEntry = { taskID: string; status: 'queued' | 'processing' };

export type TaskEventData = { task_id: string; type: string; data: any };

// ─── Extension types ───

export type ExtensionType = 'lsp' | 'ui' | 'css' | 'agent' | 'llm' | 'i18n' | 'tool';

/** An enabled extension whose contributed tools reach the network from the server process. Served with
 *  the configuration and never stored. */
export interface NetworkedExtension {
    name: string;
    display_name: string;
    tools: string[];
}

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
    network?: NetworkExtensionConfig;
    llm?: LLMExtensionConfig;
    i18n?: I18NExtensionConfig;
    /** Tools this extension contributes to the agent, declared as data. */
    tools?: ContributedTool[];
}

/** One tool an extension's manifest declares: the name the model calls and the label on its card. */
export interface ContributedTool {
    name: string;
    display_name?: string;
    description: string;
}

export interface LLMExtensionConfig {
    mappings_file?: string; // provider mappings file in the extension dir (default "mappings.json")
}

// A data-only translation pack: a message catalog and an optional prompt-template overlay,
// both inside the extension's own directory (no sandbox, mounts, network or permissions).
// A bare BCP-47 language subtag — "de", not "de-DE"; the active language is derived from
// which pack is enabled, so there is no separate language setting to agree with, and core
// holds no language names — the tag is carried through verbatim.
export interface I18NExtensionConfig {
    locale: string;
    // Catalog file, relative to the directory the installed record is served from
    // (`served_url` — /extensions/<author>/<name>/, or /extensions/<name>/ when it
    // sits flat under the extensions root). Resolved with resolveAssetURL,
    // which rejects an external URL — and the backend refuses to store one, so the two ends
    // of that fence agree.
    messages_url: string;
    // Locale-substituted prompt templates in the same directory (server-side prompt locale,
    // T14); every *.tmpl there is parse-checked at install.
    prompts_dir?: string;
}

export interface NetworkExtensionConfig {
    allowed_domains: string[];
    headers?: Record<string, string>;
}

export interface AgentExtensionConfig {
    wasm_module: string;
    capabilities?: string[];
    description?: string;    // short description shown in provider card
    setup_info?: string;     // setup instructions shown in provider card
    config_schema?: ConfigSchema;
    /** Only a folder the user approves reaches a container; this is what the manifest asks for. */
    sandbox?: { mount?: SandboxMount[]; env?: SandboxEnv[] };
}

export interface LSPConfig {
    command: string;
    args: string[];
    file_types?: LSPFileType[];
    syntax?: Record<string, SyntaxSpec>;
    language: string;
    initParamsOverride?: Record<string, any>;
    editor_enabled?: boolean;  // LSP features in the editor (diagnostics, completions, hover) — NOT syntax highlighting, which reads the manifest either way
    tool_calls_enabled?: boolean;  // LSP tools for agent (references, definition, etc.)
    chat_diagnostics_enabled?: boolean;  // After an edit, hand the file to its language server and report what it answers back to the agent. Absent means off — unlike the two above.
}

/** One file type an LSP extension owns. Mirrors extensions.LSPFileType. */
export interface LSPFileType {
    /** Bare lowercase suffix, no leading dot: "tsx". */
    extension: string;
    /** The LSP well-known identifier this file is opened as: "typescriptreact". */
    language_id: string;
    /** Human name for the editor's status bar: "TypeScript JSX". */
    label?: string;
    /** Key into LSPConfig.syntax. Absent means: fall back to a core grammar, else plain text. */
    syntax?: string;
}

/** An open/close delimiter: a block comment, a bracket pair, or a region marker. */
export interface DelimiterPair {
    open: string;
    close: string;
}

/**
 * One string form. `quote` is one character or that character repeated — Python's triple double
 * quote is a string form, while a two-character *sequence* like `<!--` is a delimiter and belongs
 * in `block_comment` or `pairs`.
 */
export interface StringDelimiter {
    quote: string;
    escapes?: boolean;
    multiline?: boolean;
}

/**
 * A grammar declared as data. The core owns the machinery — a stream tokenizer, a bracket fold
 * provider, an indent service — and everything language-shaped comes from here.
 */
export interface SyntaxSpec {
    line_comment?: string;
    /** Additional line-comment markers (PHP has `//` and `#`); `line_comment` is what Ctrl-/ uses. */
    line_comments?: string[];
    block_comment?: DelimiterPair;
    strings?: StringDelimiter[];
    /** Bracket pairs: they drive highlighting, bracket matching, folding and indentation. */
    pairs?: DelimiterPair[];
    keywords?: string[];
    types?: string[];
    constants?: string[];
    builtins?: string[];
    numbers?: boolean;
    /** Optional region markers ("#region" / "#endregion"), matched inside comment tokens. */
    region?: DelimiterPair;
    /**
     * Color a name whose first letter is upper-case as a type name (Go's exported `ChatMessage`, a
     * JS class). Default true — declare false where the convention does not hold.
     */
    capitalized_types?: boolean;
    /**
     * Color a name applied to arguments (`name(`) as a function name. Default true; the declared
     * word lists still win, so a keyword before a bracket stays a keyword.
     */
    call_as_function?: boolean;
}

export interface UIExtensionConfig {
    injection_points: string[];
    /** Fully-qualified capability names the extension requests (e.g. "createTask.description.insertText"). */
    permissions?: string[];
}

export interface CSSExtensionConfig {
    css_url: string; // global stylesheet path (resolved against the extension's own served directory; external URLs are rejected)
}

export interface ConfigSchema {
    properties?: SchemaProperty[]; // array to preserve insertion order
}

export interface SchemaProperty {
    key: string;
    type: string; // "string", "number", "boolean", "url", "mount", "env"
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
    /** How the extension checks that it can resolve a workspace at all, and which
     *  diagnostic codes mean "could not load" when it cannot. */
    environment?: { check: { command: string; args?: string[]; timeout_seconds?: number }; suppress_codes?: string[]; note?: string };
    /** Workspace files whose change makes the language server re-read the project. */
    reload_files?: string[];
    /** Where this server reads resolved dependencies from inside its own container, and the variable it reads
     *  that location from. A manifest names where it looks; the host directory that fills it is a person's
     *  choice under Settings → Extensions, and the reads always happen in the container. */
    dependency_cache?: { container_path: string; env: string };
    /**
     * The host directory the user granted to fill that cache, or empty when none is granted.
     * Reported by the server and set only through its own endpoint — a manifest cannot supply it,
     * which is the whole reason the two fields are separate.
     */
    dependency_host_path?: string;
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
    /**
     * Where the server actually serves this extension's files from
     * (`/extensions/<author>/<name>`, or `/extensions/<name>` when the extension
     * is placed flat under the extensions root). Every asset URL — a translation
     * catalog, a stylesheet, a UI module — is built from this rather than guessed
     * from the manifest's author, because the author string is not what the
     * install laid down on disk.
     */
    served_url?: string;
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
    featured?: boolean;
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

/** A single changelog entry from an extension's published CHANGELOG.jsonl. */
export interface ExtensionChangelogEntry {
    hash: string;
    version: string;
    description: string;
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
    /** The update names a host in `allowed_domains` the installed manifest does not; removing one never sets it. */
    domains_added?: boolean;
    /** Changes newer than the installed commit, oldest → newest. */
    changelog?: ExtensionChangelogEntry[];
}

/**
 * One active login session (GET /api/sessions). There is deliberately no token,
 * token-hash or CSRF field, so this shape cannot leak a credential.
 */
export interface SessionInfo {
    id: string;
    /** RFC3339 UTC login time. "" on rows predating the column. */
    created_at: string;
    /** RFC3339 UTC last authenticated request. "" when never used since login. */
    last_seen: string;
    /** RFC3339 UTC expiry. */
    expires_at: string;
    /** Client IP recorded at login. "" on rows predating the column. */
    ip: string;
    /** User-Agent recorded at login. "" on rows predating the column. */
    user_agent: string;
    /** True for the session making the request — "this device". */
    current: boolean;
}

/**
 * One API token (GET /api/tokens). There is no token field: the plaintext is handed out once, by
 * the request that created it, so a listing cannot leak a credential.
 */
export interface APITokenInfo {
    id: string;
    /** The name the user gave it — the only thing that identifies the row afterwards. */
    label: string;
    /** RFC3339 UTC issue time. */
    created_at: string;
    /** RFC3339 UTC last request it authenticated. "" when it has never been used. */
    last_used_at: string;
}

/** A created token: the row plus its secret, which is shown once and never stored in this browser. */
export interface CreatedAPIToken extends APITokenInfo {
    token: string;
}

/** What this install could do about a new release. Only `packaged` may replace its own files. */
export interface UpdateInstall {
    kind: 'packaged' | 'container' | 'source' | 'unknown';
    dir?: string;
    reason?: string;
    can_stage: boolean;
}

/** The whole update answer, which every update route returns — a failed route still answers it. */
export interface UpdateStatus {
    /** This binary's version; a development or image build reports a name no release has. */
    current_version: string;
    /** The manifest's newest release. Empty when no check has produced one — not "up to date". */
    latest?: string;
    published?: string;
    notes_url?: string;
    /** This machine's archive, absent when the newest release names none for it. */
    asset?: { url: string; sha256: string };
    /** A newer release with an archive for this machine. The dot is lit from this. */
    available: boolean;
    /** RFC3339 UTC. Empty when nothing has been checked. */
    checked_at?: string;
    /** The last check's failure, kept so the About tab can say why the dot never lit. */
    last_error?: string;
    /** What a check learned that is neither an error nor a version, e.g. a version it cannot compare. */
    note?: string;
    staged_version?: string;
    backup_path?: string;
    /** A downloaded, verified release waiting for the install click. */
    prepared_version?: string;
    prepared_bytes?: number;
    /** How many files installing it would replace. */
    prepared_files?: number;
    prepared_at?: string;
    enabled: boolean;
    /** The organization switch: nothing on this server checks, downloads, installs or restarts. */
    disabled: boolean;
    /** False while nobody has ever answered the opt-in question — the state that owes the prompt. */
    answered: boolean;
    install: UpdateInstall;
    manifest_url: string;
    /** What this server is doing right now — 'prepare' or 'apply'. Empty when neither is. */
    working?: string;
}

/** POST /api/updates/prepare: the release now downloaded and verified, installing it is still a click. */
export interface UpdatePreparedResult {
    version: string;
    bytes: number;
    sha256: string;
    /** How many files installing would replace. */
    files: number;
    notes?: string[];
}

/** POST /api/updates/apply: the release now in place. `restarting` false means finish it by hand. */
export interface UpdateApplyResult {
    version: string;
    bytes: number;
    sha256: string;
    backup_path: string;
    replaced: string[];
    notes?: string[];
    restarting: boolean;
    restart_note?: string;
}

/** POST /api/updates/restart. A process with no restart path refuses instead of answering this. */
export interface UpdateRestartResult {
    restarting: boolean;
    applies?: string;
}

