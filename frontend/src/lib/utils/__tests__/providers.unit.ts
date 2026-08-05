import { describe, it, expect } from 'vitest';
import { resolvePair, resolveWorker, resolveUtility, getModelName } from '../providers';
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
    name: 'llm',
    display_name: 'LLM',
    url: 'http://llm:8010/v1',
    kind: 'llm',
    models: [{ name: 'model-a' }, { name: 'model-b', reasoning_effort: 'high' }],
    concurrency: 1,
};

const agentProvider: Provider = {
    name: 'pi',
    display_name: 'Pi',
    url: 'agent://pi',
    kind: 'agent',
    models: [],
    concurrency: 1,
};

describe('getModelName', () => {
    it('handles string and object entries', () => {
        expect(getModelName('gpt-4o')).toBe('gpt-4o');
        expect(getModelName({ name: 'claude', reasoning_effort: 'high' })).toBe('claude');
    });
});

describe('resolvePair', () => {
    it('resolves an LLM provider/model pair', () => {
        const cfg = makeConfig({ providers: [llmProvider] });
        const r = resolvePair(cfg, 'llm', 'model-a');
        expect(r.provider?.name).toBe('llm');
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
        expect(resolvePair(null, 'llm', 'model-a').provider).toBeNull();
    });

    it('returns null provider when model not set for LLM provider', () => {
        const cfg = makeConfig({ providers: [llmProvider] });
        expect(resolvePair(cfg, 'llm', '').provider).toBeNull();
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

describe('resolveWorker / resolveUtility', () => {
    it('resolves worker pair from config', () => {
        const cfg = makeConfig({
            providers: [llmProvider, agentProvider],
            worker_provider: 'llm',
            worker_model: 'model-b',
        });
        const r = resolveWorker(cfg);
        expect(r.modelName).toBe('model-b');
        expect(r.url).toBe('http://llm:8010/v1');
    });

    it('resolves utility pair from config', () => {
        const cfg = makeConfig({
            providers: [llmProvider, agentProvider],
            utility_provider: 'pi',
            utility_model: 'pi',
        });
        const r = resolveUtility(cfg);
        expect(r.provider?.name).toBe('pi');
        expect(r.modelName).toBe('pi');
        expect(r.url).toBe('agent://pi');
    });

    it('returns empty for null config', () => {
        expect(resolveWorker(null).provider).toBeNull();
        expect(resolveUtility(null).provider).toBeNull();
    });
});
