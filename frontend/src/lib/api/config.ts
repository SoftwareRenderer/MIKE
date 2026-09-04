import type { Config } from '../types';
import { API_BASE, apiFetch, apiError } from './helpers';
import {
    getOfflineStatus,
    queueWrite,
    updateConfigRaw,
} from './queue';
import { setBoardCache, getBoardCache } from '../storage';
import { markCached } from './cache';
import { t } from '$lib/i18n';

// -- Configuration endpoints --

/** Fetch the current server configuration (LLM endpoint, model, workspace, providers). Falls back to cached copy on failure.
 * Always tries the network first — the isOffline flag tracks WebSocket state,
 * not HTTP availability. On mobile wake (tab resume), the WebSocket may be
 * disconnected but HTTP still works, so we must not short-circuit to cache. */
export async function getConfig(): Promise<Config> {
    try {
        const res = await apiFetch(`${API_BASE}/config`);
        if (!res.ok) throw await apiError(res, t('api.fetchConfig'));
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
        return await queueWrite('updateConfig', { config }, config);
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
    if (!res.ok) throw await apiError(res, t('api.testConnection'));
    return res.json();
}

/** Fetch available models from an LLM provider's /models endpoint. */
export async function fetchModels(llamaServerUrl: string, apiKey?: string): Promise<{ models: Array<{ name: string; context_window?: number }>; error?: string }> {
    const res = await apiFetch(`${API_BASE}/config/fetch-models`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ llama_server_url: llamaServerUrl, api_key: apiKey }),
    });
    if (!res.ok) throw await apiError(res, t('api.fetchModels'));
    return res.json();
}


