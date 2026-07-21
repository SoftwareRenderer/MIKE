import type { Extension, RegistryEntry, ExtensionUpdate } from '../types';
import { API_BASE, apiFetch, apiError } from './helpers';

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

/** Create a new user-defined extension. */
export async function createExtension(data: {
    name: string;
    version?: string;
    type: 'lsp' | 'ui';
    manifest?: Record<string, any>;
    config?: Record<string, any>;
    container_config?: Record<string, any>;
    sandbox_config?: Record<string, any>;
}): Promise<Extension> {
    const res = await apiFetch(`${API_BASE}/extensions`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(data),
    });
    if (!res.ok) throw await apiError(res, 'Failed to create extension');
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

/** Fetch the registry index. */
export async function getRegistryIndex(url?: string): Promise<RegistryEntry[]> {
    const params = url ? `?url=${encodeURIComponent(url)}` : '';
    const res = await apiFetch(`${API_BASE}/extensions/registry${params}`);
    if (!res.ok) throw await apiError(res, 'Failed to fetch registry');
    return res.json();
}

/** Install an extension from the registry. */
export async function installExtension(name: string, registryUrl?: string): Promise<Extension> {
    const res = await apiFetch(`${API_BASE}/extensions/install`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ name, registry_url: registryUrl }),
    });
    if (!res.ok) throw await apiError(res, 'Failed to install extension');
    return res.json();
}

/** Check for available updates. */
export async function checkUpdates(url?: string): Promise<ExtensionUpdate[]> {
    const params = url ? `?url=${encodeURIComponent(url)}` : '';
    const res = await apiFetch(`${API_BASE}/extensions/check-updates${params}`);
    if (!res.ok) throw await apiError(res, 'Failed to check for updates');
    return res.json();
}

/** Update an extension's image from the registry (rebuild or pull). */
export async function updateExtensionImage(id: string): Promise<Extension> {
    const res = await apiFetch(`${API_BASE}/extensions/${id}/update`, {
        method: 'POST',
    });
    if (!res.ok) throw await apiError(res, 'Failed to update extension');
    return res.json();
}

/** Get extension sandbox logs. */
export async function getExtensionLogs(id: string): Promise<string> {
    const res = await apiFetch(`${API_BASE}/extensions/${id}/logs`);
    if (!res.ok) throw await apiError(res, 'Failed to fetch extension logs');
    return res.text();
}

/** Build progress event from SSE stream. */
export interface BuildProgressEvent {
    extension: string;
    progress: number;
    message: string;
    done: boolean;
    error?: string;
}

/**
 * Stream build progress for an extension via SSE.
 * Returns an async iterator that yields BuildProgressEvent objects.
 */
export function streamBuildProgress(name: string): AsyncIterable<BuildProgressEvent> {
    const url = `${API_BASE}/extensions/build-progress?name=${encodeURIComponent(name)}`;

    return {
        async *[Symbol.asyncIterator]() {
            const res = await fetch(url);
            if (!res.ok) {
                throw new Error(`Failed to connect to build progress: ${res.status}`);
            }

            const reader = res.body?.getReader();
            if (!reader) {
                throw new Error('No reader available');
            }

            const decoder = new TextDecoder();
            let buffer = '';

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
                                yield JSON.parse(line.slice(6)) as BuildProgressEvent;
                            } catch {
                                // Skip malformed data
                            }
                        }
                    }
                }
            } finally {
                reader.releaseLock();
            }
        },
    };
}
