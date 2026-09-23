import { describe, it, expect } from 'vitest';
import { resolvePair, resolveWorker, getModelName, displayModelName, extractDomain, getProviderDisplayName, reselectProvider } from '../providers';
import type { Config, Provider } from '$lib/types';

function makeConfig(partial: Partial<Config>): Config {
    return {
        workspace_dir: '',
        database_path: '',
        version: 'test',
        providers: [],
        worker_provider: '',
        worker_model: '',
        utility_provider: '',
        utility_model: '',
        ...partial,
    };
}

const llmProvider: Provider = {
    id: 1,
    name: '',
    display_name: 'LLM',
    url: 'http://llm:8010/v1',
    kind: 'llm',
    models: [{ name: 'model-a' }, { name: 'model-b', reasoning_effort: 'high' }],
    concurrency: 1,
    max_requests_per_second: 2,
    stream_idle_seconds: 0,
};

const agentProvider: Provider = {
    name: 'pi',
    display_name: 'Pi',
    url: 'agent://pi',
    kind: 'agent',
    models: [],
    concurrency: 1,
    max_requests_per_second: 0,
    stream_idle_seconds: 0,
};

describe('getModelName', () => {
    it('handles string and object entries', () => {
        expect(getModelName('gpt-4o')).toBe('gpt-4o');
        expect(getModelName({ name: 'claude', reasoning_effort: 'high' })).toBe('claude');
    });
});

describe('displayModelName', () => {
    it('strips a leading orgname/ prefix', () => {
        expect(displayModelName('org/model')).toBe('model');
    });

    it('keeps non-namespaced names unchanged', () => {
        expect(displayModelName('')).toBe('');
        expect(displayModelName('llama')).toBe('llama');
    });

    it('keeps deeper nesting after the org segment', () => {
        expect(displayModelName('org/sub/model')).toBe('sub/model');
    });
});

describe('resolvePair', () => {
    it('resolves an LLM provider/model pair by its ID', () => {
        const cfg = makeConfig({ providers: [llmProvider] });
        const r = resolvePair(cfg, '1', 'model-a');
        expect(r.provider?.id).toBe(1);
        expect(r.modelName).toBe('model-a');
        expect(r.url).toBe('http://llm:8010/v1');
    });

    it('returns null provider for unknown provider', () => {
        const cfg = makeConfig({ providers: [llmProvider] });
        const r = resolvePair(cfg, 'missing', 'model-a');
        expect(r.provider).toBeNull();
        expect(r.modelName).toBe('');
        expect(r.url).toBe('');
    });

    it('returns null provider for empty name', () => {
        const cfg = makeConfig({ providers: [llmProvider] });
        expect(resolvePair(cfg, '', 'model-a').provider).toBeNull();
        expect(resolvePair(null, '1', 'model-a').provider).toBeNull();
    });

    it('returns null provider when model not set for LLM provider', () => {
        const cfg = makeConfig({ providers: [llmProvider] });
        expect(resolvePair(cfg, '1', '').provider).toBeNull();
    });

    it('agent provider model is the provider name and always valid', () => {
        const cfg = makeConfig({ providers: [agentProvider] });
        const r = resolvePair(cfg, 'pi', 'pi');
        expect(r.provider?.name).toBe('pi');
        expect(r.modelName).toBe('pi');
        expect(r.url).toBe('agent://pi');
        // Even an empty configured model resolves for agents
        const r2 = resolvePair(cfg, 'pi', '');
        expect(r2.modelName).toBe('pi');
        expect(r2.provider).not.toBeNull();
    });
});

describe('resolveWorker', () => {
    it('resolves worker pair from config by ID', () => {
        const cfg = makeConfig({
            providers: [llmProvider, agentProvider],
            worker_provider: '1',
            worker_model: 'model-b',
        });
        const r = resolveWorker(cfg);
        expect(r.modelName).toBe('model-b');
        expect(r.url).toBe('http://llm:8010/v1');
    });

        it('returns empty for null config', () => {
        expect(resolveWorker(null).provider).toBeNull();
    });
});

describe('extractDomain', () => {
    it('strips a single subdomain', () => {
        expect(extractDomain('api.example.com')).toBe('example.com');
        expect(extractDomain('neptune.example.com')).toBe('example.com');
    });

    it('keeps bare hosts and two-label domains', () => {
        expect(extractDomain('example.com')).toBe('example.com');
        expect(extractDomain('localhost')).toBe('localhost');
        expect(extractDomain('pi')).toBe('pi');
    });

    it('keeps IP addresses unchanged', () => {
        expect(extractDomain('127.0.0.1')).toBe('127.0.0.1');
        expect(extractDomain('10.0.0.1')).toBe('10.0.0.1');
    });

    it('keeps hostnames with ports unchanged', () => {
        expect(extractDomain('localhost:11434')).toBe('localhost:11434');
    });
});

describe('getProviderDisplayName', () => {
    it('does not auto-capitalize the hostname', () => {
        const p = { name: 'n', url: 'http://neptune.example.com/v1', kind: 'llm' };
        expect(getProviderDisplayName(p, [p])).toBe('example.com');
        expect(getProviderDisplayName({ ...p, url: 'http://openai.example.com/v1' }, [p])).toBe('example.com');
    });

    it('strips the subdomain when only one provider shares the domain', () => {
        const p = { name: 'n', url: 'https://api.openai.com/v1', kind: 'llm' };
        expect(getProviderDisplayName(p, [p])).toBe('openai.com');
    });

    it('keeps the subdomain when multiple providers share the same domain', () => {
        const a = { name: 'a', url: 'https://neptune.example.com/v1', kind: 'llm' };
        const b = { name: 'b', url: 'https://other.example.com/v1', kind: 'llm' };
        expect(getProviderDisplayName(a, [a, b])).toBe('neptune.example.com');
        expect(getProviderDisplayName(b, [a, b])).toBe('other.example.com');
    });

    it('omits the default port but keeps a non-default port', () => {
        expect(getProviderDisplayName({ name: 'n', url: 'https://example.com/v1', kind: 'llm' }, [])).toBe('example.com');
        expect(getProviderDisplayName({ name: 'n', url: 'http://example.com:8080/v1', kind: 'llm' }, [])).toBe('example.com:8080');
    });

    it('handles localhost and IPs without mangling', () => {
        expect(getProviderDisplayName({ name: 'n', url: 'http://localhost:11434/v1', kind: 'llm' }, [])).toBe('localhost:11434');
        expect(getProviderDisplayName({ name: 'n', url: 'http://127.0.0.1:8080/v1', kind: 'llm' }, [])).toBe('127.0.0.1:8080');
    });

    it('uses the manifest display name for agent providers', () => {
        const agent = { name: 'pi', url: 'agent://pi', kind: 'agent', display_name: 'Pi Coding Agent' };
        expect(getProviderDisplayName(agent, [agent])).toBe('Pi Coding Agent');
    });

    it('falls back to provider name when URL is invalid or empty', () => {
        expect(getProviderDisplayName({ name: 'n', url: '', kind: 'llm' }, [])).toBe('n');
        expect(getProviderDisplayName({ name: 'n', url: 'not a url', kind: 'llm' }, [])).toBe('n');
    });
});

describe('reselectProvider', () => {
    // The case that exists because of agent extensions: the provider list changes under an
    // open task chat, because the server derives agent://<name> rows from the enabled agent
    // extensions per request.
    const cfg = makeConfig({
        providers: [llmProvider, agentProvider],
        worker_provider: '1',
        worker_model: 'model-a',
    });

    it('keeps a selection whose provider is still offered', () => {
        expect(reselectProvider(cfg, cfg.providers, agentProvider.url, 'pi')).toBeNull();
        expect(reselectProvider(cfg, cfg.providers, llmProvider.url, 'model-b')).toBeNull();
    });

    it('follows the worker pair when the selected provider is gone (agent removed)', () => {
        expect(reselectProvider(cfg, [llmProvider], agentProvider.url, 'pi'))
            .toEqual({ url: llmProvider.url, model: 'model-a' });
    });

    it('selects the worker pair when nothing is selected yet (agent installed)', () => {
        expect(reselectProvider(cfg, cfg.providers, '', '')).toEqual({ url: llmProvider.url, model: 'model-a' });
    });

    it('falls back to the first provider with a model when the worker pair resolves to nothing', () => {
        const noWorker = makeConfig({ providers: [llmProvider], worker_provider: '', worker_model: '' });
        expect(reselectProvider(noWorker, noWorker.providers, '', ''))
            .toEqual({ url: llmProvider.url, model: 'model-a' });

        // Legacy rows name the worker by hand; when that name no longer resolves, the
        // selector still has to land on something selectable.
        const staleWorker = makeConfig({ providers: [llmProvider], worker_provider: 'gone', worker_model: 'x' });
        expect(reselectProvider(staleWorker, staleWorker.providers, '', ''))
            .toEqual({ url: llmProvider.url, model: 'model-a' });
    });

    it('leaves the selection alone when there is nothing to select from', () => {
        expect(reselectProvider(cfg, [], '', '')).toBeNull();
        expect(reselectProvider(null, [llmProvider], '', '')).toBeNull();
        // Providers exist but none has a model, and there is no worker pair to fall back on.
        const onlyAgent = makeConfig({ providers: [agentProvider], worker_provider: '', worker_model: '' });
        expect(reselectProvider(onlyAgent, [agentProvider], '', '')).toBeNull();
    });

    it('does not report a model for a worker pair whose provider is absent', () => {
        // resolvePair returns a null provider, so the fallback (first provider with models)
        // must be what the selector gets — never the worker's URL with an empty model.
        const dangling = makeConfig({ providers: [llmProvider], worker_provider: '99', worker_model: 'model-a' });
        expect(reselectProvider(dangling, [llmProvider], '', '')).toEqual({ url: llmProvider.url, model: 'model-a' });
    });
});
