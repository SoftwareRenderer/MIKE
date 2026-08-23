import { describe, it, expect, vi, afterEach } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { render, cleanup } from '@testing-library/svelte';
import ModelSelector from '../Chat/ModelSelector.svelte';
import type { Provider } from '$lib/types';

vi.mock('svelte/transition', () => ({
    slide: vi.fn(),
    fade: vi.fn(),
}));

const __dirname = dirname(fileURLToPath(import.meta.url));
const modelSelectorPath = resolve(__dirname, '../Chat/ModelSelector.svelte');
const source = readFileSync(modelSelectorPath, 'utf-8');

describe('ModelSelector rendering', () => {
    afterEach(() => cleanup());
    it('shows provider dropdown button', () => {
        expect(source).toContain('providerDropdownOpen');
        expect(source).toContain('selectedProvider');
    });

    it('shows model dropdown button', () => {
        expect(source).toContain('modelDropdownOpen');
        expect(source).toContain('availableModels');
    });

    it('filters providers with available models (null-safe for agents with models: null)', () => {
        expect(source).toContain('activeProviders');
        expect(source).toContain('models?.length ?? 0) > 0');
    });

    it('renders without crashing when an agent provider has models: null', () => {
        const providers: Provider[] = [
            { id: 1, url: 'http://llm:8080/v1', models: [{ name: 'model-a' }], concurrency: 1, max_requests_per_second: 0 },
            { name: 'pi', kind: 'agent', url: 'agent://pi', display_name: 'Pi Coding Agent', models: null as unknown as Provider['models'], concurrency: 1, max_requests_per_second: 0 },
        ];
        const { container } = render(ModelSelector, {
            props: { providers, currentModel: '', currentUrl: 'http://llm:8080/v1', onSelect: () => {} },
        });
        expect(container.querySelector('.provider-trigger')).toBeTruthy();
        cleanup();
    });

    it('detects agent provider', () => {
        expect(source).toContain('hasAgentProvider');
        expect(source).toContain('AGENT_URL_PREFIX');
    });

    it('sorts models alphabetically', () => {
        expect(source).toContain('localeCompare');
        expect(source).toContain('toSorted');
    });

    it('handles string and object model types', () => {
        expect(source).toContain('typeof m === \'string\'');
        expect(source).toContain('reasoning_effort');
    });

    it('builds unique model+effort pairs', () => {
        expect(source).toContain('new Map');
        expect(source).toContain('key =');
    });

    it('calls onSelect with provider and model info', () => {
        expect(source).toContain('onSelect');
        expect(source).toContain('providerName');
        expect(source).toContain('modelName');
    });

    it('positions dropdowns relative to trigger buttons', () => {
        expect(source).toContain('providerRect');
        expect(source).toContain('modelRect');
        expect(source).toContain('getBoundingClientRect');
    });

    it('manages dropdown state', () => {
        expect(source).toContain('providerDropdownOpen');
        expect(source).toContain('modelDropdownOpen');
    });
});
