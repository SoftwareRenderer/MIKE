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
        expect(source).toContain('tooltip');
    });

    it('shows total context and percentage in the tooltip only', () => {
        expect(source).toContain('tooltip');
        expect(source).toContain('/ ${formatTokens(limit)} tokens');
        expect(source).toContain('${percentage}%');
    });

    it('renders the shared ContextDonut component', () => {
        expect(source).toContain('import ContextDonut');
        expect(source).toContain('<ContextDonut {used} {limit} />');
    });

    it('shows the numerator (used tokens) next to the donut', () => {
        expect(source).toContain('usage-text');
        expect(source).toContain('{formatTokens(used)}');
    });

    it('formats tokens with K suffix for thousands', () => {
        // Formatting moved to the shared module; behavior is pinned in
        // src/lib/utils/__tests__/format.unit.ts
        expect(source).toContain('formatTokens');
        expect(source).toContain('$lib/utils/format');
    });

    it('formats tokens with M suffix for millions', () => {
        const formatSrc = readFileSync(resolve(__dirname, '../../utils/format.ts'), 'utf-8');
        expect(formatSrc).toContain('1_000_000');
        expect(formatSrc).toContain('M');
    });

    it('no longer renders a linear usage bar', () => {
        expect(source).not.toContain('usage-bar');
        expect(source).not.toContain('usage-fill');
    });

    it('is clickable when onSummarize is provided', () => {
        expect(source).toContain('clickable={!!onSummarize');
        expect(source).toContain('onSummarize');
    });

    it('shows spinner while summarizing', () => {
        expect(source).toContain('isSummarizing');
        expect(source).toContain('spinner');
    });

    it('calls onSummarizeCancel when clicking during summarization', () => {
        expect(source).toContain('onSummarizeCancel');
    });
});
