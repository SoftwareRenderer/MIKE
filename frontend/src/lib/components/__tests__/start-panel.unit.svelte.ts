import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const startPanelPath = resolve(__dirname, '../Chat/StartPanel.svelte');
const source = readFileSync(startPanelPath, 'utf-8');

describe('StartPanel rendering', () => {
    it('shows Start Task button', () => {
        expect(source).toContain('btn-start-task');
        expect(source).toContain('Start Task');
    });

    it('shows model selector when providers are available', () => {
        expect(source).toContain('providers.length > 0');
        expect(source).toContain('ModelSelector');
    });

    it('shows operating mode toggle when not in agent mode', () => {
        expect(source).toContain('!isAgentSelected()');
        expect(source).toContain('btn-plan-toggle');
    });

    it('shows Plan mode indicator', () => {
        expect(source).toContain('operatingMode === \'plan\'');
        expect(source).toContain('Plan');
    });

    it('shows Act mode indicator', () => {
        expect(source).toContain('Act');
    });

    it('shows Ideate mode indicator', () => {
        expect(source).toContain('Ideate');
        expect(source).toContain('explore');
    });

    it('disables Start Task button while starting', () => {
        expect(source).toContain('disabled={isStarting || disabled');
        expect(source).toContain('Starting...');
    });

    it('disables Start Task button when no model selected', () => {
        expect(source).toContain('!selectedModel');
    });

    it('shows spinner while starting', () => {
        expect(source).toContain('spinner-inline');
        expect(source).toContain('animation: spin');
    });

    it('has proper aria-label on mode toggle', () => {
        expect(source).toContain('aria-label="Toggle operating mode"');
    });
});
