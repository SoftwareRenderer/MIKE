import { getContext } from 'svelte';
import type { Project, Board, Config, Task } from '../types';
import { loadCollapsedTasks, saveCollapsedTasks } from '../storage';

// =============================================================================
// Types
// =============================================================================

export interface ToastData {
    message: string;
    type: 'error' | 'success';
}

export interface ConfirmOptions {
    title: string;
    message: string;
    confirmText?: string;
    cancelText?: string;
    variant?: 'default' | 'destructive';
    _resolve?: (value: boolean) => void;
}

export interface CommitDialogOptions {
    message: string;
    confirmText?: string;
    cancelText?: string;
    authorName?: string;
    authorEmail?: string;
    mergeTarget?: string;
    streaming?: boolean;
    promptProgress?: number;
    projectId?: string;
    taskId?: string;
    _resolve?: (value: string | null) => void;
}

export interface EditTaskData {
    taskId: string;
    projectId: string;
    taskTitle: string;
    taskPrompt: string;
    editProviders: Array<any>;
    editModel: string;
    editUrl: string;
    editReasoningEffort: string;
    editPlanMode: boolean;
    editAutoMerge: boolean;
    editSubtasksAutoMerge: boolean;
    taskHasParent: boolean;
    targetBranch: string;
    isAgentSelected: () => boolean;
}

export type DiffStatsEntry = { hash: string; files: number; additions: number; deletions: number; file_hashes?: Record<string, string>; removed_files?: string[]; staged_paths?: string[] };
export type DiffEntry = { hash: string; diff?: string; untracked?: Array<{ path: string; diff: string; file_size?: number; line_count?: number }>; files?: Record<string, { hash: string; type: string; added?: number; removed?: number; line_count?: number; file_size?: number }>; updated_at: number };
export type CommitQueueEntry = { taskID: string; status: 'queued' | 'processing' };
export type TaskEventData = { task_id: string; type: string; data: any };

// =============================================================================
// Context Key and Getter
// =============================================================================

const APP_CONTEXT_KEY = Symbol('app');

export interface AppContext {
    projects: Project[];
    selectedProject: Project | null;
    board: Board | null;
    config: Config | null;
    selectedTask: Task | null;
    sidebarCollapsed: boolean;
    showCreateTask: boolean;
    creatingChildOf: string | null;
    showConfig: boolean;
    showTerminal: boolean;
    showStats: boolean;
    showCardDetail: boolean;
    showSandboxConfig: boolean;
    showEditTask: boolean;
    editTaskData: EditTaskData | null;
    toast: ToastData | null;
    confirmation: ConfirmOptions | null;
    commitDialog: CommitDialogOptions | null;
    pwaInstallPrompt: Event | null;
    pwaInstalled: boolean;
    streamingTaskIds: Set<string>;
    taskDiffStats: Record<string, DiffStatsEntry>;
    taskDiff: Record<string, DiffEntry>;
    commitQueueStatus: Record<string, CommitQueueEntry>;
    expandedTasks: Set<string>;
    taskEvent: TaskEventData | null;
    showToast: (message: string, type?: 'error' | 'success', duration?: number) => void;
    showConfirm: (options: ConfirmOptions) => Promise<boolean>;
    showCommitDialog: (options: CommitDialogOptions) => Promise<string | null>;
    persistCollapsedTasks: (expanded: Set<string>, parentTaskIds: Set<string>) => Promise<void>;
    cleanupCollapsedTasks: (allTaskIds: Set<string>) => Promise<Set<string>>;
    initExpandedTasks: (parentTaskIds: Set<string>) => Promise<Set<string>>;
}

export function getAppContext(): AppContext {
    return getContext(APP_CONTEXT_KEY);
}

export { APP_CONTEXT_KEY };

// Helper functions (not reactive, just storage utilities)
export async function persistCollapsedTasks(expanded: Set<string>, parentTaskIds: Set<string>): Promise<void> {
    const collapsed = new Set<string>();
    for (const id of parentTaskIds) {
        if (!expanded.has(id)) collapsed.add(id);
    }
    await saveCollapsedTasks(collapsed);
}

export async function cleanupCollapsedTasks(allTaskIds: Set<string>): Promise<Set<string>> {
    const collapsed = await loadCollapsedTasks();
    let changed = false;
    for (const id of collapsed) {
        if (!allTaskIds.has(id)) { collapsed.delete(id); changed = true; }
    }
    if (changed) await saveCollapsedTasks(collapsed);
    return collapsed;
}

export async function initExpandedTasks(parentTaskIds: Set<string>): Promise<Set<string>> {
    const collapsed = await loadCollapsedTasks();
    for (const id of collapsed) { if (!parentTaskIds.has(id)) collapsed.delete(id); }
    await saveCollapsedTasks(collapsed);
    const expanded = new Set<string>();
    for (const id of parentTaskIds) { if (!collapsed.has(id)) expanded.add(id); }
    return expanded;
}
