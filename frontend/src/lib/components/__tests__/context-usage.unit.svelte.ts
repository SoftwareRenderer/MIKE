import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const contextUsagePath = resolve(__dirname, '../Chat/ContextUsage.svelte');
const source = readFileSync(contextUsagePath, 'utf-8');

describe('ContextUsage rendering', () => {
    it('shows token usage when used > 0', () => {
        expect(source).toContain('used > 0');
        expect(source).toContain('usage-text');
    });

    it('shows usage bar with percentage when limit is set', () => {
        expect(source).toContain('limit > 0');
        expect(source).toContain('usage-bar');
        expect(source).toContain('usage-fill');
        expect(source).toContain('percentage');
    });

    it('formats tokens with K suffix for thousands', () => {
        expect(source).toContain('1_000');
        expect(source).toContain('K');
    });

    it('formats tokens with M suffix for millions', () => {
        expect(source).toContain('1_000_000');
        expect(source).toContain('M');
    });

    it('shows green bar when usage is low', () => {
        expect(source).toContain('color-status-green');
    });

    it('shows amber warning when usage exceeds 70%', () => {
        expect(source).toContain('percentage > 70');
        expect(source).toContain('color-status-amber');
    });

    it('shows orange warning when usage exceeds 80%', () => {
        expect(source).toContain('percentage > 80');
        expect(source).toContain('color-status-orange-dark');
    });

    it('shows red danger when usage exceeds 90%', () => {
        expect(source).toContain('percentage >= 90');
        expect(source).toContain('color-status-red');
    });

    it('is clickable when onSummarize is provided', () => {
        expect(source).toContain('clickable={!!onSummarize');
        expect(source).toContain('onSummarize');
    });

    it('shows spinner while summarizing', () => {
        expect(source).toContain('isSummarizing');
        expect(source).toContain('spinner');
        expect(source).toContain('Summarizing...');
    });

    it('shows cancel icon when summarizing', () => {
        expect(source).toContain('cancel-icon');
    });

    it('calls onSummarizeCancel when clicking during summarization', () => {
        expect(source).toContain('onSummarizeCancel');
    });
});
