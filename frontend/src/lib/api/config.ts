import type { Config } from '../types';
import { API_BASE, apiFetch, apiError } from './helpers';
import {
    getOfflineStatus,
    enqueueAction,
    updateConfigRaw,
    reloadPromptsRaw,
} from './queue';

// -- Configuration endpoints --

/** Fetch the current server configuration (LLM endpoint, model, workspace, providers). */
export async function getConfig(): Promise<Config> {
    const res = await apiFetch(`${API_BASE}/config`);
    if (!res.ok) throw await apiError(res, 'Failed to fetch config');
    return res.json();
}

/** Update server configuration. Sends partial updates — only changed fields are required. Queued when offline. */
export async function updateConfig(config: Config): Promise<Config> {
    if (getOfflineStatus()) {
        enqueueAction({ type: 'updateConfig', params: { config } });
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
        enqueueAction({ type: 'reloadPrompts', params: {} });
        return { status: 'queued', message: 'Queued', files: [] };
    }
    return reloadPromptsRaw();
}
