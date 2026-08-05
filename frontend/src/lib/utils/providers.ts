import type { Config, Provider } from '$lib/types';
import { isAgentProvider } from '$lib/types';

/** A resolved provider/model pair: the provider, the selected model name, and its URL. */
export interface ResolvedPair {
    provider: Provider | null;
    modelName: string;
    url: string;
}

/** Normalize a model entry (legacy string or object) to its name. */
export function getModelName(m: string | { name: string; reasoning_effort?: string }): string {
    return typeof m === 'string' ? m : m.name;
}

/**
 * Resolve a configured provider/model pair (worker or utility) from the config.
 * Looks up the provider by name and validates that the model exists in its list.
 * For agent providers the model is the agent's own name and always valid.
 * Returns null provider when the pair cannot be resolved.
 */
export function resolvePair(cfg: Config | null, providerName: string, modelName: string): ResolvedPair {
    if (!cfg || !providerName) {
        return { provider: null, modelName: '', url: '' };
    }
    const provider = cfg.providers.find(p => p.name === providerName) || null;
    if (!provider) {
        return { provider: null, modelName: '', url: '' };
    }
    // Agent providers don't have a real model list; the model is the agent name.
    if (isAgentProvider(provider)) {
        return { provider, modelName: providerName, url: provider.url };
    }
    if (!modelName) {
        return { provider: null, modelName: '', url: '' };
    }
    return { provider, modelName, url: provider.url };
}

/** Resolve the configured Worker provider/model pair. */
export function resolveWorker(cfg: Config | null): ResolvedPair {
    if (!cfg) return { provider: null, modelName: '', url: '' };
    return resolvePair(cfg, cfg.worker_provider, cfg.worker_model);
}

/** Resolve the configured Utility provider/model pair. */
export function resolveUtility(cfg: Config | null): ResolvedPair {
    if (!cfg) return { provider: null, modelName: '', url: '' };
    return resolvePair(cfg, cfg.utility_provider, cfg.utility_model);
}
