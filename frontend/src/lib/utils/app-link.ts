/**
 * The deep links the link-like rows, buttons and file rows carry. They must match what `updateUrl`
 * writes in `App.svelte` (project, task, view, file) and what `applyUrlView` reads back.
 */

/** The screen that shows the terminal alone, with no board, top bar or sidebar behind it. */
export const TERMINAL_VIEW = 'terminal';

/** The two explorer screens `applyUrlView` reads back; which one depends on whether a task is named. */
const EXPLORER_VIEW = 'explorer';
const TASK_EXPLORER_VIEW = 'task-explorer';

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

/**
 * A path as the `file` param writes it: `/` stays literal, as `updateUrl` writes it, and every other
 * character is encoded so a segment holding `&` or `#` cannot add a param of its own. A directory
 * keeps its trailing `/`, which is what tells the reader to list it rather than open it.
 */
function fileParam(path: string, isDir: boolean): string {
    const encoded = path.split('/').map(encodeURIComponent).join('/');
    return isDir && !encoded.endsWith('/') ? encoded + '/' : encoded;
}

/** An explorer opened on one path — the link a file row hands a new window. */
export function explorerPathHref(projectId: string, taskId: string, path: string, isDir: boolean): string {
    const params = 'project=' + encodeURIComponent(projectId)
        + (taskId ? '&task=' + encodeURIComponent(taskId) + '&view=' + TASK_EXPLORER_VIEW : '&view=' + EXPLORER_VIEW)
        + '&file=' + fileParam(path, isDir);
    return location.pathname + '?' + params;
}

/** What a `file` param names: a folder when it ends in `/`, the path without that slash otherwise. */
export function parseExplorerFile(file: string): { path: string; isDir: boolean } {
    const isDir = file.endsWith('/');
    return { path: isDir ? file.slice(0, -1) : file, isDir };
}

/** Where a `file` param has to live: it is honoured only when it is inside the explorer that opens. */
export interface ExplorerPathRoots {
    projectPath: string;
    projectName: string;
    sandboxEnabled: boolean;
    workspaceDir?: string;
    projectId?: string;
    /** A URL that names no task has no worktree to accept. */
    taskId?: string;
}

/**
 * Every root a `file` param may sit under: the project checkout, the named task's worktree, and the
 * mount each of those appears under inside a container — the project's, named after its directory, and
 * the task's, named after the project. The two explorers admit the same set through
 * `ExplorerCore.isPathSafe`, which clamps again where the path is opened.
 */
export function explorerPathRoots(roots: ExplorerPathRoots): string[] {
    const base = roots.projectPath.split('/').filter(Boolean).pop() ?? '';
    const worktree = roots.workspaceDir && roots.projectId && roots.taskId
        ? roots.workspaceDir + '/' + roots.projectId + '/' + roots.taskId
        : '';
    const mounts = !roots.sandboxEnabled ? [] : [
        base && '/home/sandbox/' + base,
        roots.projectName && '/home/sandbox/' + roots.projectName,
    ];
    return [...new Set([roots.projectPath, worktree, ...mounts].filter(Boolean))];
}

export function fileWithinRoots(file: string, roots: string[]): boolean {
    return !!file && roots.some(root => file.startsWith(root));
}
