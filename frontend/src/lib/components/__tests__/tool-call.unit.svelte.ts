import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const toolCallPath = resolve(__dirname, '../Chat/ToolCall.svelte');
const source = readFileSync(toolCallPath, 'utf-8');

describe('ToolCall rendering', () => {
    it('shows tool display name', () => {
        expect(source).toContain('getToolDisplayName');
        expect(source).toContain('displayName');
    });

    it('shows Editing label for edit_file tool calls', () => {
        expect(source).toContain('edit_file');
        expect(source).toContain('Editing');
    });

    it('shows +X/-Y toggle for edit_file tool calls', () => {
        expect(source).toContain('linesAdded');
        expect(source).toContain('linesRemoved');
    });

    it('shows sandbox indicator for sandboxable tools', () => {
        expect(source).toContain('sandbox');
        expect(source).toContain('SANDBOXABLE_TOOLS');
    });

    it('shows tool result with markdown rendering', () => {
        expect(source).toContain('marked');
        expect(source).toContain('result');
    });

    it('shows success/failure status', () => {
        expect(source).toContain('success');
        expect(source).toContain('check-circle');
    });

    it('shows duration for completed tool calls', () => {
        expect(source).toContain('duration');
        expect(source).toContain('formatElapsed');
    });

    it('shows pending state for in-progress tool calls', () => {
        expect(source).toContain('isPending');
    });

    it('hides args for certain tools', () => {
        expect(source).toContain('HIDE_ARGS_FOR_TOOLS');
    });

    it('truncates long args', () => {
        expect(source).toContain('ARGS_MAX_LEN');
        expect(source).toContain('formatToolArgs');
    });

    it('shows diff for edit_file tool calls', () => {
        expect(source).toContain('buildModifiedSegments');
        expect(source).toContain('EDIT_DIFF_MAX_LINES');
    });

    it('fetches full result when truncated', () => {
        expect(source).toContain('resultTruncated');
        expect(source).toContain('getTaskMessageFull');
    });

    it('shows bytes written for write_file tool calls', () => {
        expect(source).toContain('bytesWritten');
    });

    it('accumulates raw_args_delta for streaming tool calls', () => {
        expect(source).toContain('raw_args');
    });

    it('shows tool call with slide transition', () => {
        expect(source).toContain('slide');
        expect(source).toContain('transition');
    });
});
