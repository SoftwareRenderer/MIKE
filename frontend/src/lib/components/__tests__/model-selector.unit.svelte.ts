import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const modelSelectorPath = resolve(__dirname, '../Chat/ModelSelector.svelte');
const source = readFileSync(modelSelectorPath, 'utf-8');

describe('ModelSelector rendering', () => {
    it('shows provider dropdown button', () => {
        expect(source).toContain('providerDropdownOpen');
        expect(source).toContain('selectedProvider');
    });

    it('shows model dropdown button', () => {
        expect(source).toContain('modelDropdownOpen');
        expect(source).toContain('availableModels');
    });

    it('filters providers with available models', () => {
        expect(source).toContain('activeProviders');
        expect(source).toContain('models.length > 0');
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
