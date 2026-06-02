// TypeScript interfaces matching backend Go types

export interface Project {
    id: string;
    name: string;
    path: string;
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
    max_consecutive_failed_turns: number;
    auto_commit: boolean;
    auto_pr: boolean;
    plan_mode: boolean;
    created_at: string;
    updated_at: string;
}

export interface Board {
    tasks: Task[];
}

export interface Config {
    llama_server_url: string;
    model_name: string;
    workspace_dir: string;
    endpoint_limits: Record<string, number>;
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
    | 'error';

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
    tokens: { prompt: number; completed: number; total: number };
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
        args: Record<string, unknown>;
        result?: string;
        success?: boolean;
        duration?: number;
    };
    reasoningSummary?: string;
    tokenCount?: number;
}