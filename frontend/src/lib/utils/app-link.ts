/**
 * The deep links the link-like buttons carry. They must match what `updateUrl` writes in
 * `App.svelte` (project, task, view) and what `applyUrlView` reads back.
 */

/** The screen that shows the terminal alone, with no board, top bar or sidebar behind it. */
export const TERMINAL_VIEW = 'terminal';

function link(projectId: string, taskId: string | null): string {
    const params = 'project=' + encodeURIComponent(projectId)
        + (taskId ? '&task=' + encodeURIComponent(taskId) : '')
        + '&view=' + TERMINAL_VIEW;
    return location.pathname + '?' + params;
}

export function projectTerminalHref(projectId: string): string {
    return link(projectId, null);
}

/** The `task` param is what makes this the task's shell rather than the project's. */
export function taskTerminalHref(projectId: string, taskId: string): string {
    return link(projectId, taskId);
}

/** The task view itself: `project` plus `task`, and no `view` param at all. */
export function taskViewHref(projectId: string, taskId: string): string {
    return location.pathname + '?project=' + encodeURIComponent(projectId) + '&task=' + encodeURIComponent(taskId);
}

/**
 * A project's board — the link a sidebar row carries, and the one a notification about several tasks
 * opens, since no single task is what it speaks for.
 */
export function projectBoardHref(projectId: string): string {
    return location.pathname + '?project=' + encodeURIComponent(projectId);
}

export function asksForTerminal(search: string): boolean {
    return new URLSearchParams(search).get('view') === TERMINAL_VIEW;
}
