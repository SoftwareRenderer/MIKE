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

/** The build stream's last frame: `sandbox.BuildResult` marshalled. Every other frame of that stream is a plain
 *  line for the log, and `status` is what tells the result from a line that happens to start with `{`. */
export interface SandboxBuildResult {
    status: string;
    image: string;
    commit: string;
    from: string;
    bases: string[];
    cache_key: string;
    base_image_id?: string;
    files: number;
    scratch_peak?: string;
}

/**
 *  Stream build progress via SSE, one line per frame. A plain fetch rather than
 *  `apiFetch` for the reason `streamCommitPreview` (`api/tasks.ts`) states. */
export async function buildSandboxImageStream(
    id: string,
    onLine: (line: string) => void,
): Promise<SandboxBuildResult> {
    const headers = await csrfHeaders();

    const url = `${API_BASE}/projects/${id}/sandbox/build`;
    const res = await fetch(url, {
        method: 'POST',
        credentials: 'include',
        headers,
    });
    if (!res.ok) {
        const err = await res.json().catch(() => ({}));
        throw new Error(err.error || t('api.buildSandboxImage'));
    }

    let result: SandboxBuildResult | null = null;
    // The server reports a failure as a final `error: …` line, because it answered 200 before the build ran.
    // The thrown error carries that line as its cause.
    let cause = '';

    for await (const frame of readEventStream(res)) {
        const data = frame.data;
        if (data.startsWith('error: ')) cause = data.slice('error: '.length);
        // The final result is the JSON object carrying a `status` string; everything else is a line for the
        // log, including a `{`-shaped line that is not valid JSON or is JSON that names no result.
        if (data.startsWith('{')) {
            try {
                const parsed = JSON.parse(data);
                if (parsed && typeof parsed === 'object' && typeof parsed.status === 'string') {
                    result = parsed as SandboxBuildResult;
                    continue;
                }
            } catch { }
        }
        onLine(data);
    }

    if (!result) throw new Error(cause || t('api.operationEndedEarly'));
    return result;
}

/** One frame of the watch stream, which is the server's `types.SandboxImageStatus`: what the preparation is
 *  doing, the image it is for, and the line it printed. The last frame says how it ended. */
export interface SandboxImageStatus {
    phase: string;
    image?: string;
    line?: string;
    error?: string;
}

/**
 * Watch the build or pull this project's image is running, and read every status it announces from now on.
 * The result is false when nothing is preparing the image.
 *
 * This is plain streaming rather than a queued write, for the reason `buildSandboxImageStream` states. The
 * signal stops the reading and nothing else.
 */
export async function watchSandboxImageBuild(
    id: string,
    onStatus: (status: SandboxImageStatus) => void,
    signal?: AbortSignal,
): Promise<boolean> {
    const res = await apiFetch(`${API_BASE}/projects/${id}/sandbox/build`, { credentials: 'include', signal });
    if (res.status === 404) return false;
    if (!res.ok) throw await apiError(res, t('api.watchSandboxImage'));

    for await (const frame of readEventStream(res)) {
        if (!frame.data.startsWith('{')) continue;
        try {
            const status = JSON.parse(frame.data) as SandboxImageStatus;
            if (typeof status.phase === 'string') onStatus(status);
        } catch { /* a frame that is not a status carries nothing this screen can draw */ }
    }
    return true;
}

/**
 * Stop the build or pull this project's image is running, and report whether the click stopped anything.
 *
 * This is not `writeOrQueue`: a cancel replayed when the connection comes back would stop whatever build is
 * running by then, which is not the one the person pressed the button about.
 */
export async function cancelSandboxImageBuild(id: string): Promise<boolean> {
    const res = await apiFetch(`${API_BASE}/projects/${id}/sandbox/build/cancel`, { method: 'POST' });
    if (!res.ok) throw await apiError(res, t('api.cancelSandboxImage'));
    const body = await res.json().catch(() => ({ cancelled: false }));
    return body?.cancelled === true;
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
