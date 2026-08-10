import type { Extension, RegistryEntry, ExtensionUpdate } from '../types';
import { API_BASE, apiFetch, apiError, ensureCsrfToken } from './helpers';

/** Fetch all installed extensions with runtime status. */
export async function listExtensions(): Promise<Extension[]> {
    const res = await apiFetch(`${API_BASE}/extensions`);
    if (!res.ok) throw await apiError(res, 'Failed to fetch extensions');
    return res.json();
}

/** Fetch a single extension by ID. */
export async function getExtension(id: string): Promise<Extension> {
    const res = await apiFetch(`${API_BASE}/extensions/${id}`);
    if (!res.ok) throw await apiError(res, 'Failed to fetch extension');
    return res.json();
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
    if (!res.ok) throw await apiError(res, 'Failed to update extension');
    return res.json();
}

/** Delete an extension and destroy its sandbox. */
export async function deleteExtension(id: string): Promise<void> {
    const res = await apiFetch(`${API_BASE}/extensions/${id}`, {
        method: 'DELETE',
    });
    if (!res.ok) throw await apiError(res, 'Failed to delete extension');
}

/** Toggle the enabled state of an extension. */
export async function setExtensionEnabled(id: string, enabled: boolean): Promise<Extension> {
    const res = await apiFetch(`${API_BASE}/extensions/${id}/enabled`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ enabled }),
    });
    if (!res.ok) throw await apiError(res, 'Failed to update extension enabled state');
    return res.json();
}

/** Fetch extensions present in the local extensions directory (filesystem). */
export async function listLocalExtensions(): Promise<RegistryEntry[]> {
    const res = await apiFetch(`${API_BASE}/extensions/local`);
    if (!res.ok) throw await apiError(res, 'Failed to fetch local extensions');
    return res.json();
}

/** Fetch the registry index. */
export async function getRegistryIndex(url?: string): Promise<RegistryEntry[]> {
    const params = url ? `?url=${encodeURIComponent(url)}` : '';
    const res = await apiFetch(`${API_BASE}/extensions/registry${params}`);
    if (!res.ok) throw await apiError(res, 'Failed to fetch registry');
    return res.json();
}

/** Stream SSE progress + final result; the install survives a client disconnect. */
export function installExtension(name: string, registryUrl?: string): AsyncIterable<BuildProgressEvent> {
    return streamOperation('/extensions/install', { name, registry_url: registryUrl });
}

/** Install a local (unpublished) extension from the local extensions directory. */
export function installLocalExtension(author: string, name: string): AsyncIterable<BuildProgressEvent> {
    return streamOperation('/extensions/install-local', { author, name });
}

/** Check for available updates. */
export async function checkUpdates(url?: string): Promise<ExtensionUpdate[]> {
    const params = url ? `?url=${encodeURIComponent(url)}` : '';
    const res = await apiFetch(`${API_BASE}/extensions/check-updates${params}`);
    if (!res.ok) throw await apiError(res, 'Failed to check for updates');
    return res.json();
}

/** Stream SSE progress + final result for an image update (detached, like install). */
export function updateExtensionImage(id: string): AsyncIterable<BuildProgressEvent> {
    return streamOperation(`/extensions/${id}/update`, {});
}

/** Get extension sandbox logs. */
export async function getExtensionLogs(id: string): Promise<string> {
    const res = await apiFetch(`${API_BASE}/extensions/${id}/logs`);
    if (!res.ok) throw await apiError(res, 'Failed to fetch extension logs');
    return res.text();
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
        if (!res.ok) throw await apiError(res, 'Operation failed');

        // The server streams SSE; surface a clear error if it returns a plain body instead.
        const contentType = res.headers?.get('content-type') ?? '';
        // Enforce SSE content-type only when the header is readable (some test mocks omit it).
        if (contentType && !contentType.toLowerCase().includes('text/event-stream')) {
            const text = await res.text();
            throw new Error(
                `Unexpected response from ${path} (${contentType}): ${text.slice(0, 200) || 'empty body'}`
            );
        }

        const reader = res.body?.getReader();
        if (!reader) {
            throw new Error('No reader available');
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
                throw new Error('Operation ended before reporting completion');
            }
        } finally {
            reader.releaseLock();
        }
    };

    return { [Symbol.asyncIterator]: iterator };
}
