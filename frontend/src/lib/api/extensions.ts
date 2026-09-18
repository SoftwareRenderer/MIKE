import type { Extension, RegistryEntry, ExtensionUpdate } from '../types';
import { API_BASE, apiFetch, apiError, ensureCsrfToken } from './helpers';
import { t } from '$lib/i18n';

/** Fetch all installed extensions with runtime status. */
export async function listExtensions(): Promise<Extension[]> {
    const res = await apiFetch(`${API_BASE}/extensions`);
    if (!res.ok) throw await apiError(res, t('api.fetchExtensions'));
    return res.json();
}

// `config` carries the form's current values, so an unsaved address and an unsaved token are both tested.
// Never queued when offline: a replayed probe answers a question nobody is still asking.
export async function testExtensionUrl(
    id: string,
    body: { key: string; url: string; config?: Record<string, any> }
): Promise<ExtensionUrlProbe> {
    const res = await apiFetch(`${API_BASE}/extensions/${id}/test-url`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(body),
    });
    if (!res.ok) throw await apiError(res, t('api.testExtensionUrl'));
    return res.json();
}

/** One address an extension's tools send to, as the dialog reports it. */
export interface ExtensionUrlProbe {
    success: boolean;
    message: string;
}

/** Update an existing extension. */
export async function updateExtension(id: string, data: {
    version?: string;
    manifest?: Record<string, any>;
    config?: Record<string, any>;
    container_config?: Record<string, any>;
    sandbox_config?: Record<string, any>;
}): Promise<Extension> {
    const res = await apiFetch(`${API_BASE}/extensions/${id}`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(data),
    });
    if (!res.ok) throw await apiError(res, t('api.updateExtension'));
    return res.json();
}

/** Delete an extension and destroy its sandbox. */
export async function deleteExtension(id: string): Promise<DeleteExtensionResult> {
    const res = await apiFetch(`${API_BASE}/extensions/${id}`, {
        method: 'DELETE',
    });
    if (!res.ok) throw await apiError(res, t('api.deleteExtension'));
    return (await res.json().catch(() => ({}))) as DeleteExtensionResult;
}

/** `leftover` is set when the extension was deleted but its materialized files could not be removed —
 * files that are still served and still offered for install, and that nothing else in the UI can see. */
export interface DeleteExtensionResult {
    status?: string;
    leftover?: string;
}

/** Toggle the enabled state of an extension. */
export async function setExtensionEnabled(id: string, enabled: boolean): Promise<Extension> {
    const res = await apiFetch(`${API_BASE}/extensions/${id}/enabled`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ enabled }),
    });
    if (!res.ok) throw await apiError(res, t('api.updateExtensionEnabledState'));
    return res.json();
}

/**
 * Grant (or revoke, with an empty path) the one host directory an extension's language server
 * reads its resolved dependencies from. An empty string is a real value here — it means revoke —
 * which is why this is its own call rather than a field of the sandbox form.
 */
export async function setDependencyCache(id: string, hostPath: string): Promise<Extension> {
    const res = await apiFetch(`${API_BASE}/extensions/${id}/dependency-cache`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ host_path: hostPath }),
    });
    if (!res.ok) throw await apiError(res, t('api.updateDependencyDirectory'));
    return res.json();
}

/** An empty list clears the approval. */
export async function setAgentMount(
    id: string,
    mounts: { host_path: string; container_path: string; mode: string }[],
): Promise<Extension> {
    const res = await apiFetch(`${API_BASE}/extensions/${id}/agent-mount`, {
        method: 'PUT',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ mounts }),
    });
    if (!res.ok) throw await apiError(res, t('api.setAgentMount'));
    return res.json();
}

/** The server's own home directory, for suggesting where a dependency directory should live. */
export async function getHomeDirectory(): Promise<string> {
    const res = await apiFetch(`${API_BASE}/home`);
    if (!res.ok) throw await apiError(res, t('api.readHomeDirectory'));
    const data = await res.json();
    return typeof data?.home === 'string' ? data.home : '';
}

/** Fetch extensions present in the local extensions directory (filesystem). */
export async function listLocalExtensions(): Promise<RegistryEntry[]> {
    const res = await apiFetch(`${API_BASE}/extensions/local`);
    if (!res.ok) throw await apiError(res, t('api.fetchLocalExtensions'));
    return res.json();
}

/** Fetch the registry index. */
export async function getRegistryIndex(): Promise<RegistryEntry[]> {
    const res = await apiFetch(`${API_BASE}/extensions/registry`);
    if (!res.ok) throw await apiError(res, t('api.fetchRegistry'));
    return res.json();
}

/** Stream SSE progress + final result; the install survives a client disconnect. */
export function installExtension(name: string): AsyncIterable<BuildProgressEvent> {
    return streamOperation('/extensions/install', { name });
}

/** Install a local (unpublished) extension from the local extensions directory. */
export function installLocalExtension(author: string, name: string): AsyncIterable<BuildProgressEvent> {
    return streamOperation('/extensions/install-local', { author, name });
}

/** Check for available updates. */
export async function checkUpdates(): Promise<ExtensionUpdate[]> {
    const res = await apiFetch(`${API_BASE}/extensions/check-updates`);
    if (!res.ok) throw await apiError(res, t('api.checkForUpdates'));
    return res.json();
}

/** Stream SSE progress + final result for an image update (detached, like install). */
/** Pass true only after the user accepts a version that can reach a new host; the server refuses
 * that update without it. */
export function updateExtensionImage(id: string, acknowledgeNewDomains = false): AsyncIterable<BuildProgressEvent> {
    return streamOperation(`/extensions/${id}/update`, { acknowledge_new_domains: acknowledgeNewDomains });
}

/** Build progress event from an operation's SSE stream. */
export interface BuildProgressEvent {
    extension: string;
    progress: number;
    message: string;
    done: boolean;
    error?: string;
    /** The installed/updated extension on the final `done` event. */
    result?: Extension;
}

/** POST to a streaming endpoint and parse its SSE response; HTTP errors are thrown. */
function streamOperation(path: string, body: Record<string, any>): AsyncIterable<BuildProgressEvent> {
    const url = `${API_BASE}${path}`;

    const iterator = async function* () {
        const token = await ensureCsrfToken();
        const headers: Record<string, string> = { 'Content-Type': 'application/json' };
        if (token) headers['X-CSRF-Token'] = token;

        const res = await fetch(url, {
            method: 'POST',
            credentials: 'include',
            headers,
            body: JSON.stringify(body),
        });
        if (!res.ok) throw await apiError(res, t('api.operationFailed'));

        // The server streams SSE; surface a clear error if it returns a plain body instead.
        const contentType = res.headers?.get('content-type') ?? '';
        // Enforce SSE content-type only when the header is readable (some test mocks omit it).
        if (contentType && !contentType.toLowerCase().includes('text/event-stream')) {
            const text = await res.text();
            throw new Error(
                t('api.unexpectedResponse', { path, contentType, body: text.slice(0, 200) || t('api.emptyBody') })
            );
        }

        const reader = res.body?.getReader();
        if (!reader) {
            throw new Error(t('api.noReader'));
        }

        const decoder = new TextDecoder();
        let buffer = '';
        let sawDone = false;

        try {
            while (true) {
                const { done, value } = await reader.read();
                if (done) break;

                buffer += decoder.decode(value, { stream: true });

                // Parse SSE events
                const lines = buffer.split('\n');
                buffer = lines.pop() || ''; // Keep incomplete line in buffer

                for (const line of lines) {
                    if (line.startsWith('data: ')) {
                        try {
                            const event = JSON.parse(line.slice(6)) as BuildProgressEvent;
                            if (event.done) sawDone = true;
                            yield event;
                        } catch {
                            // Skip malformed data
                        }
                    }
                }
            }

            // A healthy SSE stream always ends with a `done` event.
            if (!sawDone) {
                throw new Error(t('api.operationEndedEarly'));
            }
        } finally {
            reader.releaseLock();
        }
    };

    return { [Symbol.asyncIterator]: iterator };
}
