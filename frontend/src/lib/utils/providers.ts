import type { Config, Provider } from '$lib/types';
import { isAgentProvider, AGENT_URL_PREFIX } from '$lib/types';
import { t } from '$lib/i18n';

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
 * Shorten a namespaced model name for compact display (e.g. the task view selector
 * label). If a model is formatted as "orgname/model", drop the leading "orgname/"
 */
export function displayModelName(name: string): string {
    if (!name) return name;
    const slash = name.indexOf('/');
    return slash > 0 ? name.slice(slash + 1) : name;
}

/**
 * The reference string for a provider: its stable integer ID (LLM) or name (agent).
 */
export function providerRef(p: { id?: number; name?: string; kind?: string; url?: string }): string {
    return p.kind === 'agent' || (p.url || '').startsWith(AGENT_URL_PREFIX)
        ? (p.name || '')
        : String(p.id ?? 0);
}

/**
 * Resolve a configured provider/model pair (worker or utility) from the config.
 * The reference is the provider's stable integer ID (as a string) for LLM providers,
 * or the provider name for agent providers. For agent providers the model is the
 * agent's own name and always valid. Returns null provider when the pair cannot be resolved.
 *
 * @knipignore A pair is resolved per config shape; `resolveWorker` reads only the worker fields of one.
 */
export function resolvePair(cfg: Config | null, ref: string, modelName: string): ResolvedPair {
    if (!cfg || !ref) {
        return { provider: null, modelName: '', url: '' };
    }
    const provider = cfg.providers.find(p => {
        if (isAgentProvider(p)) return p.name === ref;
        // LLM providers are referenced by their stable ID; fall back to the
        // (legacy) name for backward compatibility.
        return String(p.id) === ref || (!!ref && p.name === ref);
    }) || null;
    if (!provider) {
        return { provider: null, modelName: '', url: '' };
    }
    // Agent providers don't have a real model list; the model is the agent name.
    if (isAgentProvider(provider)) {
        return { provider, modelName: provider.name || '', url: provider.url };
    }
    if (!modelName) {
        return { provider: null, modelName: '', url: '' };
    }
    return { provider, modelName, url: provider.url };
}

/**
 * What a provider selector should hold once the provider **list** has changed — the case an
 * open task chat hits when an agent extension is installed or removed while it is looking at
 * the config (`agent://<name>` rows appear and disappear with the enabled extensions).
 *
 * The current selection wins whenever its provider is still offered: neither a task's own
 * model nor a choice the user just made is ours to second-guess. Otherwise fall back to the
 * configured worker pair, then to the first provider that has a model at all.
 * `null` means "leave the selection exactly as it is".
 */
export function reselectProvider(
    cfg: Config | null,
    providers: Provider[],
    currentUrl: string,
    currentModel: string,
): { url: string; model: string } | null {
    if (!cfg || providers.length === 0) return null;
    if (currentModel && currentUrl && providers.some(p => p.url === currentUrl)) return null;
    const worker = resolveWorker(cfg);
    if (worker.provider && worker.modelName) return { url: worker.url, model: worker.modelName };
    const first = providers.find(p => (p.models?.length ?? 0) > 0);
    return first ? { url: first.url, model: getModelName(first.models[0]) } : null;
}

/** Resolve the configured Worker provider/model pair. */
export function resolveWorker(cfg: Config | null): ResolvedPair {
    if (!cfg) return { provider: null, modelName: '', url: '' };
    return resolvePair(cfg, cfg.worker_provider, cfg.worker_model);
}

/**
 * The provider URL and model one task actually runs on: its own when set, otherwise the worker default.
 * A subtask inherits from the worker configuration and never from the task above it, so any comparison
 * between a subtask and its parent has to resolve both sides rather than diff two fields.
 */
export function effectiveModel(
    task: { llama_server_url?: string; model_name?: string } | undefined,
    cfg: Config | null,
): { url: string; model: string } {
    const worker = resolveWorker(cfg);
    return { url: task?.llama_server_url || worker.url, model: task?.model_name || worker.modelName };
}

/** The subset of provider fields needed to derive a display name. */
export interface DisplayableProvider {
    name?: string;
    url: string;
    kind?: string;
    display_name?: string;
}

/**
 * Extract the registrable domain from a hostname, dropping the subdomain (e.g. api.example.com → example.com).
 *
 * @knipignore The rule is asserted per host, including the hosts with no subdomain to drop.
 */
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
        return provider.display_name || provider.name || t('settings.provider.agentFallback');
    }
    const url = (provider.url || '').trim();
    if (!url) return provider.name || t('settings.provider.providerFallback');
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
        return provider.name || t('settings.provider.providerFallback');
    }
}
