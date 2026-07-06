import type { Config } from '../types';
import { API_BASE, apiFetch, apiError } from './helpers';
import {
    getOfflineStatus,
    enqueueAction,
    updateConfigRaw,
    reloadPromptsRaw,
} from './queue';
import { setBoardCache, getBoardCache } from '../storage';

/** Runtime marker on cached data to indicate it was served from the offline mirror. */
const CACHE_MARKER = Symbol('fromCache');
export function isFromCache(obj: unknown): boolean {
    return (obj as any)?.[CACHE_MARKER] === true;
}
function markCached<T>(data: T): T {
    return Object.assign(data, { [CACHE_MARKER]: true });
}

// -- Configuration endpoints --

/** Fetch the current server configuration (LLM endpoint, model, workspace, providers). Falls back to cached copy on failure or when offline. */
export async function getConfig(): Promise<Config> {
    // When offline, skip network and read cache directly
    if (getOfflineStatus()) {
        const cached = await getBoardCache<Config>('config');
        if (cached) return markCached(cached);
        throw new Error('No cached config available');
    }
    try {
        const res = await apiFetch(`${API_BASE}/config`);
        if (!res.ok) throw await apiError(res, 'Failed to fetch config');
        const data = await res.json();
        // Cache fire-and-forget
        setBoardCache('config', data).catch(() => {});
        return data;
    } catch (e) {
        const cached = await getBoardCache<Config>('config');
        if (cached) return markCached(cached);
        throw e;
    }
}

/** Update server configuration. Sends partial updates — only changed fields are required. Queued when offline. */
export async function updateConfig(config: Config): Promise<Config> {
    if (getOfflineStatus()) {
        await enqueueAction({ type: 'updateConfig', params: { config } });
        return config;
    }
    return updateConfigRaw(config);
}

/** Test connectivity to an LLM provider by sending a minimal chat completion request. */
export async function testConnection(llamaServerUrl: string, modelName: string, apiKey?: string): Promise<{ success: boolean; message: string }> {
    const res = await apiFetch(`${API_BASE}/config/test-connection`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ llama_server_url: llamaServerUrl, model_name: modelName, api_key: apiKey }),
    });
    if (!res.ok) throw await apiError(res, 'Failed to test connection');
    return res.json();
}

/** Fetch available models from an LLM provider's /models endpoint. */
export async function fetchModels(llamaServerUrl: string, apiKey?: string): Promise<{ models: Array<{ name: string; context_window?: number }>; error?: string }> {
    const res = await apiFetch(`${API_BASE}/config/fetch-models`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ llama_server_url: llamaServerUrl, api_key: apiKey }),
    });
    if (!res.ok) throw await apiError(res, 'Failed to fetch models');
    return res.json();
}

/** Test Pi coding agent connection by running `pi --version`. */
export async function testPiConnection(): Promise<{ success: boolean; message: string }> {
    const res = await apiFetch(`${API_BASE}/config/test-pi-connection`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({}),
    });
    if (!res.ok) throw await apiError(res, 'Failed to test Pi connection');
    return res.json();
}

/** Reload prompt templates from disk. Queued when offline. */
export async function reloadPrompts(): Promise<{ status: string; message: string; files: string[] }> {
    if (getOfflineStatus()) {
        await enqueueAction({ type: 'reloadPrompts', params: {} });
        return { status: 'queued', message: 'Queued', files: [] };
    }
    return reloadPromptsRaw();
}
