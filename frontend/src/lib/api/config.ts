import type { Config } from '../types';
import { API_BASE, apiFetch, apiError } from './helpers';
import {
    getOfflineStatus,
    queueWrite,
} from './queue';
import { updateConfigRaw } from './raw/config';
import { setBoardCache, getBoardCache } from '../storage';
import { markCached } from './cache';
import { t } from '$lib/i18n';

// -- Configuration endpoints --

/** Fetch the current server configuration (LLM endpoint, model, workspace, providers). Falls back to cached copy on failure.
 * Always tries the network first: a read is what releases the queue (`api/queue.ts`). */
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

/**
 * The placeholder the server answers in place of a stored provider key
 * (maskedAPIKey, backend/config/config.go). Sending it back means "keep the stored
 * key", so it is the only api_key value that is not a secret. Written as escapes:
 * it is compared, never displayed.
 */
const MASKED_API_KEY = '\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022\u2022';

/** Whether this payload carries a provider key that is an actual secret. */
function carriesPlaintextAPIKey(config: Config): boolean {
    return (config.providers ?? []).some(
        (p) => typeof p.api_key === 'string' && p.api_key.trim() !== '' && p.api_key !== MASKED_API_KEY,
    );
}

/** Update server configuration. Sends partial updates — only changed fields are required. Queued when offline. */
export async function updateConfig(config: Config): Promise<Config> {
    if (getOfflineStatus()) {
        // The queue is persisted, so a key queued here outlives the tab and the
        // session. Everything else about the save still queues; stripping the key
        // from the queued body instead would quietly lose what the user typed.
        if (carriesPlaintextAPIKey(config)) {
            throw new Error(t('api.updateConfigOfflineKey'));
        }
        return await queueWrite('updateConfig', { config }, config);
    }
    return updateConfigRaw(config);
}

/** Answer the release-check question outside the Settings form (the first-run prompt does). A partial
 *  body: PUT /api/config writes only the fields it names. */
export async function setUpdateCheckConsent(enabled: boolean): Promise<void> {
    await updateConfig({ update_check_enabled: enabled } as Config);
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


