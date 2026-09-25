// One domain's plain HTTP calls: the offline queue replays these, and nothing here knows the queue exists.
import type { Project, ProjectUpdates } from '../../types';
import { API_BASE, apiFetch, apiError, csrfHeaders, readEventStream } from '../helpers';
import { t } from '$lib/i18n';

export async function createProjectRaw(path: string, sandboxEnabled = false, gitUrl?: string): Promise<Project> {
    const body: Record<string, unknown> = { path, sandbox_enabled: sandboxEnabled };
    if (gitUrl) {
        body.git_url = gitUrl;
    }
    if (sandboxEnabled) {
        body.sandbox_network_isolation = true;
    }
    const res = await apiFetch(`${API_BASE}/projects`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
    });
    if (!res.ok) throw await apiError(res, t('api.createProject'));
    return res.json();
}

export async function updateProjectRaw(id: string, updates: ProjectUpdates): Promise<Project> {
    const res = await apiFetch(`${API_BASE}/projects/${id}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(updates),
    });
    if (!res.ok) throw await apiError(res, t('api.updateProject'));
    return res.json();
}

/** Stream build progress via SSE. onLine is called for each progress line.
 *  opts.noCache skips the container build layer cache for this build only. A plain fetch rather than
 *  `apiFetch` for the reason `streamCommitPreview` (`api/tasks.ts`) states. */
export async function buildSandboxImageStream(
    id: string,
    onLine: (line: string) => void,
    opts?: { noCache?: boolean },
): Promise<{ status: string; image: string; project: string }> {
    const headers = await csrfHeaders();

    const url = `${API_BASE}/projects/${id}/sandbox/build${opts?.noCache ? '?no_cache=1' : ''}`;
    const res = await fetch(url, {
        method: 'POST',
        credentials: 'include',
        headers,
    });
    if (!res.ok) {
        const err = await res.json().catch(() => ({}));
        throw new Error(err.error || t('api.buildSandboxImage'));
    }

    let result: { status: string; image: string; project: string } | null = null;

    for await (const frame of readEventStream(res)) {
        const data = frame.data;
        // The final result is the JSON object carrying `status`; everything else is a line for the
        // log, including a `{`-shaped line that is not valid JSON.
        if (data.startsWith('{')) {
            try {
                const parsed = JSON.parse(data);
                if (parsed && typeof parsed === 'object' && 'status' in parsed) {
                    result = parsed;
                    continue;
                }
            } catch {}
        }
        onLine(data);
    }

    if (!result) throw new Error(t('api.buildCompletedWithoutResult'));
    return result;
}

export async function deleteProjectRaw(id: string): Promise<void> {
    const res = await apiFetch(`${API_BASE}/projects/${id}`, { method: 'DELETE' });
    if (!res.ok) throw await apiError(res, t('api.deleteProject'));
}

export async function resetProjectStatsRaw(projectId: string, modelName: string, providerName?: string): Promise<void> {
    const params = new URLSearchParams({ model_name: modelName });
    if (providerName) params.set('provider_name', providerName);
    const res = await apiFetch(`${API_BASE}/projects/${projectId}/stats?${params}`, { method: 'DELETE' });
    if (!res.ok) throw await apiError(res, t('api.resetStats'));
}
