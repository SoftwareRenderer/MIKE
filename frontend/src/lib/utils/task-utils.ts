import type { Task } from '$lib/types';

/** Merge an update into a task, preserving existing fields when the update is queued.
 * Queued updates are optimistic local changes that may be incomplete and should
 * be shallow-merged into the existing task rather than replacing it entirely. */
export function mergeQueuedUpdate(task: Task, updated: Partial<Task>): Task {
    return (updated as any).queued ? { ...task, ...updated } : updated as Task;
}

/**
 * True while the executor can still write to this task's transcript. The vocabulary is
 * `backend/types/exec_state.go`'s (mirrored server-side by `terminalTurnStates`), and a
 * task's `status` is never the answer: stopping one keeps `in_progress` and only moves
 * `execution_state` to `paused`.
 */
export function taskIsInFlight(executionState: string): boolean {
    return executionState === 'running' || executionState === 'pulling_image';
}

/** Recursively count a task and all its descendants that match the given status. */
export function countWithDescendants(taskId: string, status: string, childrenMap: Map<string, Task[]>): number {
    const children = childrenMap.get(taskId) ?? [];
    let count = 1;
    for (const child of children) {
        if (child.status === status) {
            count += countWithDescendants(child.id, status, childrenMap);
        }
    }
    return count;
}

/** True while some ancestor of this task sits in In Progress. The server refuses the same shape
 * per request (`checkAncestorInProgress` in `backend/api/task.go`). */
export function ancestorInProgress(tasks: Task[], taskId: string): boolean {
    const byId = new Map(tasks.map(t => [t.id, t]));
    const seen = new Set<string>([taskId]);
    let currentId = byId.get(taskId)?.parent_task_id ?? '';
    while (currentId && !seen.has(currentId)) {
        seen.add(currentId);
        const ancestor = byId.get(currentId);
        if (!ancestor) break;
        if (ancestor.status === 'in_progress') return true;
        currentId = ancestor.parent_task_id ?? '';
    }
    return false;
}

/** True when a subtask moved to `targetStatus` joins the column its parent is already in, and is
 * folded into the parent's card. An optimistic board update must skip it or the card vanishes
 * mid-action. */
export function willConsolidateIntoParent(task: Task, tasks: Task[], targetStatus: string): boolean {
    return !!task.parent_task_id && tasks.some(t => t.id === task.parent_task_id && t.status === targetStatus);
}

export interface BulkMovePlan {
    /** The moves to send, ancestors before their descendants. */
    movable: string[];
    /** Selected tasks the server would refuse; reported to the user instead of sent. */
    blocked: string[];
    alreadyThere: number;
}

/**
 * Which of a batch move's selections to send, and in what order. Nothing already in the target column
 * is sent; the rest is ordered ancestors-first because the server answers each request from the state
 * the previous one left behind, so an ancestor this batch moves out of In Progress stops blocking.
 */
export function planBulkMove(tasks: Task[], taskIds: string[], targetStatus: string): BulkMovePlan {
    const byId = new Map(tasks.map(t => [t.id, t]));
    const candidates: string[] = [];
    let alreadyThere = 0;
    for (const id of taskIds) {
        const task = byId.get(id);
        if (!task) continue;
        if (task.status === targetStatus) {
            alreadyThere++;
            continue;
        }
        candidates.push(id);
    }

    const decided = new Map<string, boolean>();
    const movable: string[] = [];
    const blocked: string[] = [];
    const depth = (id: string): number => {
        const seen = new Set<string>([id]);
        let current = byId.get(id);
        let d = 0;
        while (current?.parent_task_id && !seen.has(current.parent_task_id) && byId.has(current.parent_task_id)) {
            seen.add(current.parent_task_id);
            current = byId.get(current.parent_task_id);
            d++;
        }
        return d;
    };

    // Ordering by depth puts every ancestor before its descendants.
    const depthOf = new Map(candidates.map(id => [id, depth(id)]));
    for (const id of [...candidates].sort((a, b) => (depthOf.get(a) ?? 0) - (depthOf.get(b) ?? 0))) {
        const task = byId.get(id)!;
        const moves = targetStatus !== 'backlog' || !task.parent_task_id ||
            !heldByInProgressAncestor(task, byId, decided);
        decided.set(id, moves);
        (moves ? movable : blocked).push(id);
    }
    return { movable, blocked, alreadyThere };
}

/** Whether an ancestor keeps this subtask out of Backlog: it is In Progress, and this batch does not
 * move it out of there. An id the batch never decided keeps its status, so it holds. */
function heldByInProgressAncestor(
    task: Task,
    byId: Map<string, Task>,
    decided: Map<string, boolean>,
): boolean {
    const seen = new Set<string>([task.id]);
    let currentId = task.parent_task_id ?? '';
    while (currentId && !seen.has(currentId)) {
        seen.add(currentId);
        const ancestor = byId.get(currentId);
        if (!ancestor) break;
        if (ancestor.status === 'in_progress' && !decided.get(currentId)) return true;
        currentId = ancestor.parent_task_id ?? '';
    }
    return false;
}

/** Column the mobile board shows after switching projects: the one with the most urgent
 * work — Review, else In Progress, else Backlog. Tasks hidden inside a collapsed parent
 * card still count. */
export function resolveBoardLandingColumn(tasks: Pick<Task, 'status'>[]): 'review' | 'in_progress' | 'backlog' {
    if (tasks.some(t => t.status === 'review')) return 'review';
    if (tasks.some(t => t.status === 'in_progress')) return 'in_progress';
    return 'backlog';
}

/**
 * Column the board lands on when a task view closes: Review if it holds work other than the closed
 * task, else the column the task moved into. The closed task is excluded because the board list on
 * screen still holds its pre-move status until the update lands.
 */
export function resolveClosedTaskColumn(
    status: string,
    tasks: Pick<Task, 'id' | 'status'>[],
    closedTaskId: string,
): string {
    if (status !== 'done') return status;
    return tasks.some(t => t.id !== closedTaskId && t.status === 'review') ? 'review' : status;
}

/** Reduce a set of task IDs to those whose parent is NOT also in the set.
 * Descendants are excluded because the server recursively deletes them */
export function filterTopLevelTaskIds(taskIds: string[], tasks: Task[]): string[] {
    const selected = new Set(taskIds);
    const taskById = new Map(tasks.map(t => [t.id, t]));
    return taskIds.filter(id => {
        const task = taskById.get(id);
        if (!task?.parent_task_id) return true;
        return !selected.has(task.parent_task_id);
    });
}

/**
 * Apply a `task_updated` frame to the board on screen, or return null when there is nothing to apply.
 *
 * A task the board has never held is appended only when the frame names this project: the server
 * replays the board frames it buffered for this connection, and those include projects the client
 * switched away from. A task already on the board is updated whatever the frame claims, and one whose
 * project the frame does not name is never appended — refreshing a row we already hold is safe,
 * adding a card of unknown ownership is not.
 */
export function mergeTaskUpdateIntoBoard(tasks: Task[], updated: Task, projectId: string): Task[] | null {
    const idx = tasks.findIndex(t => t.id === updated.id);
    if (idx < 0) return updated.project_id === projectId ? [...tasks, updated] : null;
    if (tasks[idx] === updated) return null;
    return tasks.map((t, i) => (i === idx ? updated : t));
}
