import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const queueIndicatorPath = resolve(__dirname, '../Chat/QueueIndicator.svelte');
const source = readFileSync(queueIndicatorPath, 'utf-8');

describe('QueueIndicator rendering', () => {
    it('shows spinner animation while waiting', () => {
        expect(source).toContain('class="spinner"');
        expect(source).toContain('animation: spin');
    });

    it('shows queue position when in queue', () => {
        expect(source).toContain('queuePosition');
        expect(source).toContain('position');
        expect(source).toContain('in queue');
    });

    it('shows estimated wait time', () => {
        expect(source).toContain('queueEstimatedWait');
    });

    it('shows prompt processing progress', () => {
        expect(source).toContain('promptProgress');
        expect(source).toContain('Processing Prompt');
    });

    it('shows retry count when connection issues occur', () => {
        expect(source).toContain('retryCount');
        expect(source).toContain('retry');
        expect(source).toContain('Connection issue');
    });

    it('shows retry error message when available', () => {
        expect(source).toContain('retryError');
    });

    it('uses agent label when agent mode is selected', () => {
        expect(source).toContain('isAgentSelected');
        expect(source).toContain('agent');
    });

    it('uses LLM provider label when not in agent mode', () => {
        expect(source).toContain('LLM provider');
    });

    it('applies retry variant styling when retrying', () => {
        expect(source).toContain('class:retry={variant() === \'retry\'}');
        expect(source).toContain('.awaiting-llm.retry');
    });
});
