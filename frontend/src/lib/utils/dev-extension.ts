import type { Extension } from '$lib/types';

/**
 * The development row a task's project owns, or null when nothing here is a custom extension. A row of
 * another project is never an answer for this task, whatever its name.
 */
export function devRowForProject(extensions: Extension[], projectId: string): Extension | null {
    if (!projectId) return null;
    for (const ext of extensions) {
        if (ext.install_source === 'dev' && ext.project_id === projectId) return ext;
    }
    return null;
}
