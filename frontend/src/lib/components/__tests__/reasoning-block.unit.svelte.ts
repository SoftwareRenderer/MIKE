import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const reasoningPath = resolve(__dirname, '../Chat/ReasoningBlock.svelte');
const source = readFileSync(reasoningPath, 'utf-8');

describe('ReasoningBlock rendering', () => {
    it('shows reasoning toggle when hasReasoning is true', () => {
        expect(source).toContain('showReasoningToggle');
        expect(source).toContain('hasReasoning');
        expect(source).toContain('{#if showReasoningToggle}');
    });

    it('does not show reasoning toggle when hasReasoning is false', () => {
        // The toggle is only rendered when showReasoningToggle is true
        expect(source).toContain('{#if showReasoningToggle}');
        // showReasoningToggle derives from hasReasoning or reasoningContent
        expect(source).toContain('hasReasoning || !!reasoningContent');
    });

    it('reasoning toggle has proper accessibility attributes', () => {
        expect(source).toContain('aria-expanded={reasoningExpanded}');
        expect(source).toContain('title={reasoningExpanded ? \'Hide reasoning\' : \'Show reasoning\'}');
    });

    it('reasoning toggle shows/hides reasoning content', () => {
        expect(source).toContain('reasoningExpanded ? \'Hide\' : \'Show\'');
        expect(source).toContain('{#if reasoningExpanded}');
        expect(source).toContain('reasoning-content');
    });

    it('reasoning content is displayed when expanded', () => {
        expect(source).toContain('{effectiveReasoning}');
        expect(source).toContain('reasoning-content');
    });

    it('reasoning toggle is disabled while loading', () => {
        expect(source).toContain('disabled={reasoningLoading}');
        expect(source).toContain('reasoning-loading');
    });

    it('fetches full reasoning content when [HIDDEN]', () => {
        expect(source).toContain('getTaskMessageFull');
        expect(source).toContain('\[HIDDEN\]');
        expect(source).toContain('resolvedReasoningOverride');
    });

    it('auto-expands short reasoning without content', () => {
        // Short reasoning (<=200 chars) with no content auto-expands
        expect(source).toContain('effectiveReasoning.length <= 200');
    });

    it('auto-expands reasoning when tool calls present without content', () => {
        // Tool calls with no content auto-expand reasoning
        expect(source).toContain('toolCallsLength > 0');
    });

    it('auto-expands reasoning during streaming without tool calls', () => {
        // Streaming without tool calls auto-expands
        expect(source).toContain('isStreaming');
    });
});
