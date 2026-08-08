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

/** The subset of provider fields needed to derive a display name. */
export interface DisplayableProvider {
    name: string;
    url: string;
    kind?: string;
    display_name?: string;
}

/** Extract the registrable domain from a hostname, dropping the subdomain (e.g. api.example.com → example.com). */
export function extractDomain(host: string): string {
    // IP addresses, localhost, and bare hosts have no subdomain to strip.
    if (host === 'localhost' || host.includes(':') || /^\d{1,3}(\.\d{1,3}){3}$/.test(host)) return host;
    const parts = host.split('.');
    if (parts.length <= 2) return host;
    return parts.slice(-2).join('.');
}

/**
 * Derive a provider's display name dynamically. Display names are never cached.
 * Agent providers use their (manifest-derived) name; LLM providers derive from the URL:
 * the hostname is shown as-is (no auto-capitalization), and the subdomain is only included
 * when multiple providers share the same registrable domain (to keep them distinguishable).
 */
export function getProviderDisplayName(provider: DisplayableProvider, allProviders: DisplayableProvider[]): string {
    if (isAgentProvider(provider)) {
        return provider.display_name || provider.name || 'Agent';
    }
    const url = (provider.url || '').trim();
    if (!url) return provider.name || 'Provider';
    try {
        const u = new URL(url);
        const host = u.hostname;
        const defaultPort = u.protocol === 'https:' ? '443' : '80';
        const port = u.port || defaultPort;
        const domain = extractDomain(host);
        const sameDomainCount = allProviders.filter(other => {
            try { return extractDomain(new URL(other.url).hostname) === domain; }
            catch { return false; }
        }).length;
        const displayHost = sameDomainCount > 1 ? host : domain;
        // Omit the default port (e.g. 443 for https, 80 for http) from the heading.
        return port === defaultPort ? displayHost : `${displayHost}:${port}`;
    } catch {
        return provider.name || 'Provider';
    }
}
