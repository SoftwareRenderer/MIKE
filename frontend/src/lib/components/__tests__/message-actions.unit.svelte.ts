import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const messageActionsPath = resolve(__dirname, '../Chat/MessageActions.svelte');
const source = readFileSync(messageActionsPath, 'utf-8');

describe('MessageActions rendering', () => {
    it('shows copy button for user messages', () => {
        expect(source).toContain('variant === \'user\'');
        expect(source).toContain('aria-label="Copy message"');
        expect(source).toContain('onclick={onCopy}');
    });

    it('shows copy button for assistant messages with content', () => {
        // Assistant messages show copy button only when hasContent is true
        expect(source).toContain('{#if hasContent}');
        expect(source).toContain('aria-label="Copy message"');
    });

    it('shows edit button for user messages when onFork is provided', () => {
        expect(source).toContain('{#if onFork}');
        expect(source).toContain('aria-label="Start new thread from this message"');
    });

    it('shows delete button for user messages when onDelete is provided', () => {
        expect(source).toContain('{#if onDelete}');
        expect(source).toContain('aria-label="Delete message and all following messages"');
    });

    it('shows edit button for assistant messages when onEdit is provided and not streaming', () => {
        expect(source).toContain('{#if onEdit && !isStreaming && !isOptimistic}');
        expect(source).toContain('aria-label="Edit message"');
    });

    it('shows regenerate button for assistant messages when onRegenerate is provided', () => {
        expect(source).toContain('{#if onRegenerate && !isStreaming && !isOptimistic}');
        expect(source).toContain('aria-label="Regenerate response"');
    });

    it('shows visual feedback when copied', () => {
        expect(source).toContain('class:copied={copied}');
        expect(source).toContain('class="check"');
    });

    it('does not show edit/regenerate buttons while streaming', () => {
        expect(source).toContain('!isStreaming');
    });

    it('has user-actions class for user variant', () => {
        expect(source).toContain('class:user-actions={variant === \'user\'}');
    });

    it('has assistant-actions class for assistant variant', () => {
        expect(source).toContain('class:assistant-actions={variant === \'assistant\'}');
    });
});
