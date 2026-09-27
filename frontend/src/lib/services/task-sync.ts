import { getTaskSync } from '$lib/api';
import type { TaskSyncDigest } from '$lib/types';

/**
 * One digest request per task per stretch of returning, whatever number of views came back with it. The entry
 * goes as soon as the answer lands, so the next return asks again rather than reusing a stale digest.
 */
const inFlight = new Map<string, Promise<TaskSyncDigest | null>>();

/** The digest, or null when it could not be had. */
export function fetchTaskSync(projectId: string, taskId: string): Promise<TaskSyncDigest | null> {
    const key = `${projectId}:${taskId}`;
    const already = inFlight.get(key);
    if (already) return already;
    const call = getTaskSync(projectId, taskId)
        .catch(() => null)
        .finally(() => {
            if (inFlight.get(key) === call) inFlight.delete(key);
        });
    inFlight.set(key, call);
    return call;
}
