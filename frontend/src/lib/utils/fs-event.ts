/**
 * What a watcher's `fs_event` says about a path.
 *
 * `WorktreeWatcher` names the path relative to one task's worktree (`src/main.go`),
 * `ProjectWatcher` relative to the project directory
 * (`worktrees/<projectId>/<taskId>/src/main.go`).
 */
export interface FsEvent {
    op: 'create' | 'delete';
    path: string;
}

/**
 * The event path as one task's worktree sees it, or `null` when it belongs to another task of the
 * same project. A path with neither prefix is already task-relative.
 */
export function taskRelativeFsPath(
    event: FsEvent,
    projectId: string,
    taskId: string,
): string | null {
    if (!event.path) return null;
    const projectPrefix = `worktrees/${projectId}/`;
    const ownPrefix = `${projectPrefix}${taskId}/`;
    if (event.path.startsWith(ownPrefix)) return event.path.slice(ownPrefix.length);
    if (event.path.startsWith(projectPrefix)) return null;
    return event.path;
}

/**
 * Whether an event could concern `filePath`, which the explorers address as
 * `<root>/<path relative to that root>`.
 *
 * A host path and its sandbox mount are not tellable apart here, so without a matching root the
 * tails are compared: a wrong `false` would strand a buffer that says something the disk no longer
 * does.
 */
export function fsEventConcernsFile(
    event: FsEvent,
    filePath: string,
    opts: { projectId?: string; taskId?: string; rootPath?: string } = {},
): boolean {
    const { projectId, taskId, rootPath } = opts;
    if (!event.path || !filePath) return true;
    const relative = taskId && projectId
        ? taskRelativeFsPath(event, projectId, taskId)
        : event.path;
    if (relative === null) return false;
    if (rootPath && filePath.startsWith(`${rootPath}/`)) {
        return filePath.slice(rootPath.length + 1) === relative;
    }
    return filePath === relative || filePath.endsWith(`/${relative}`);
}
