import { describe, it, expect, vi } from 'vitest';
import { render, screen } from '@testing-library/svelte';
import ToolCall from '$lib/components/Chat/ToolCall.svelte';

// Mock dependencies
vi.mock('$lib/components/Icon.svelte', () => ({
    default: () => '',
}));
vi.mock('$lib/assets/icons/shield-green.svg?raw', () => ({ default: '' }));
vi.mock('$lib/assets/icons/shield-off.svg?raw', () => ({ default: '' }));
vi.mock('$lib/assets/icons/zap.svg?raw', () => ({ default: '' }));
vi.mock('$lib/assets/icons/file-text.svg?raw', () => ({ default: '' }));
vi.mock('$lib/assets/icons/search.svg?raw', () => ({ default: '' }));
vi.mock('$lib/assets/icons/edit.svg?raw', () => ({ default: '' }));
vi.mock('$lib/assets/icons/check-circle.svg?raw', () => ({ default: '' }));
vi.mock('$lib/assets/icons/question.svg?raw', () => ({ default: '' }));
vi.mock('marked', () => ({
    marked: { parse: (text: string) => `<p>${text}</p>` },
}));
vi.mock('dompurify', () => ({
    default: { sanitize: (html: string) => html },
}));
vi.mock('$lib/utils/diff-parser', () => ({
    buildModifiedSegments: () => ({ oldSegments: [], newSegments: [] }),
}));
vi.mock('$lib/api', () => ({
    getTaskMessageFull: vi.fn(),
}));

vi.mock('$lib/types', () => ({
    getToolDisplayName: (displayName: string | undefined, toolName: string) => displayName || toolName.replace('_', ' '),
}));

vi.mock('$lib/utils/toolArgs', () => ({
    formatToolArgs: (toolName: string, args: Record<string, unknown> | null, rawArgs?: string) => {
        if (!args) return '';
        if (toolName === 'bash') return `$ ${args.command || ''}`;
        if (toolName === 'read_file' || toolName === 'write_file') return args.path as string || '';
        if (toolName === 'edit_file') {
            const p = args.path as string;
            const edits = args.edits as Array<{ oldText: string; newText: string }>;
            const count = edits ? edits.length : 0;
            return count > 0 ? `${p} (${count} edit${count > 1 ? 's' : ''})` : p || '';
        }
        return JSON.stringify(args);
    },
    HIDE_ARGS_FOR_TOOLS: new Set(['create_task', 'edit_task', 'view_tasks', 'request_review', 'ask_question']),
    SANDBOXABLE_TOOLS: new Set(['read_file', 'write_file', 'edit_file', 'bash']),
    ARGS_MAX_LEN: 50,
    EDIT_DIFF_MAX_LINES: 20,
}));

const baseProps = {
    expandedArgs: new Set<string>(),
    editDiffOverrides: {},
    onToggleArgs: vi.fn(),
    onToggleEditDiff: vi.fn(),
};

function makeToolCall(overrides = {}) {
    return {
        toolId: 'tool-1',
        toolName: 'bash',
        args: { command: 'ls' },
        result: undefined,
        success: true,
        isPending: false,
        sandbox: false,
        duration: undefined,
        bytesWritten: undefined,
        path: undefined,
        raw_args: undefined,
        resultTruncated: false,
        messageId: undefined,
        startedAt: undefined,
        ...overrides,
    };
}

describe('ToolCall - pending status text', () => {
    it('shows "Editing..." for pending edit_file', () => {
        const { container } = render(ToolCall, {
            ...baseProps,
            toolCall: makeToolCall({
                toolName: 'edit_file',
                args: { path: 'src/file.go', edits: [{ oldText: 'a', newText: 'b' }] },
                isPending: true,
            }),
        });
        expect(container.querySelector('.progress-text')?.textContent).toBe('Editing...');
    });

    it('shows "Writing..." for pending write_file', () => {
        const { container } = render(ToolCall, {
            ...baseProps,
            toolCall: makeToolCall({
                toolName: 'write_file',
                args: { path: 'src/file.go' },
                isPending: true,
            }),
        });
        expect(container.querySelector('.progress-text')?.textContent).toBe('Writing...');
    });

    it('shows "Preparing: path" for pending read_file', () => {
        const { container } = render(ToolCall, {
            ...baseProps,
            toolCall: makeToolCall({
                toolName: 'read_file',
                args: { path: 'src/file.go' },
                path: 'src/file.go',
                isPending: true,
            }),
        });
        expect(container.querySelector('.progress-text')?.textContent).toBe('Preparing: src/file.go');
    });

    it('shows "Running" for executing bash', () => {
        const { container } = render(ToolCall, {
            ...baseProps,
            toolCall: makeToolCall({
                toolName: 'bash',
                args: { command: 'ls' },
                isPending: true,
            }),
        });
        expect(container.querySelector('.progress-text')?.textContent).toBe('Running');
    });

    it('shows "Writing Summary" for pending request_review', () => {
        const { container } = render(ToolCall, {
            ...baseProps,
            toolCall: makeToolCall({
                toolName: 'request_review',
                args: { summary: 'Done' },
                isPending: true,
            }),
        });
        expect(container.querySelector('.progress-text')?.textContent).toBe('Writing Summary');
    });

    it('shows "Writing Question" for pending ask_question', () => {
        const { container } = render(ToolCall, {
            ...baseProps,
            toolCall: makeToolCall({
                toolName: 'ask_question',
                args: { question: 'What next?' },
                isPending: true,
            }),
        });
        expect(container.querySelector('.progress-text')?.textContent).toBe('Writing Question');
    });

    it('shows elapsed time when running longer than 5s', () => {
        const startedAt = Date.now() - 10000;
        const { container } = render(ToolCall, {
            ...baseProps,
            toolCall: makeToolCall({
                toolName: 'write_file',
                args: { path: 'src/file.go' },
                isPending: true,
                startedAt,
            }),
        });
        const text = container.querySelector('.progress-text')?.textContent;
        expect(text).toContain('Running for');
    });
});

describe('ToolCall - completed status', () => {
    it('shows "Completed" for successful tool calls', () => {
        const { container } = render(ToolCall, {
            ...baseProps,
            toolCall: makeToolCall({
                toolName: 'write_file',
                args: { path: 'src/file.go' },
                success: true,
                isPending: false,
            }),
        });
        const status = container.querySelector('.tool-status.success');
        expect(status?.textContent).toBe('Completed');
    });

    it('shows "Failed" for failed tool calls', () => {
        const { container } = render(ToolCall, {
            ...baseProps,
            toolCall: makeToolCall({
                toolName: 'write_file',
                args: { path: 'src/file.go' },
                success: false,
                isPending: false,
            }),
        });
        const status = container.querySelector('.tool-status.error');
        expect(status?.textContent).toBe('Failed');
    });
});

describe('ToolCall - edit_file diff toggle', () => {
    it('shows +X/-Y toggle for edit_file tool calls', () => {
        const { container } = render(ToolCall, {
            ...baseProps,
            toolCall: makeToolCall({
                toolName: 'edit_file',
                args: { path: 'src/file.go', edits: [{ oldText: 'a\nb', newText: 'c\nd' }] },
                success: true,
                isPending: false,
            }),
        });
        const toggle = container.querySelector('.edit-diff-toggle');
        expect(toggle).toBeTruthy();
        expect(toggle?.textContent).toMatch(/\+\d+\s*\/\s*-\d+/);
    });
});

describe('ToolCall - write_file result', () => {
    it('shows bytes written for completed write_file', () => {
        const { container } = render(ToolCall, {
            ...baseProps,
            toolCall: makeToolCall({
                toolName: 'write_file',
                args: { path: 'src/file.go' },
                path: 'src/file.go',
                bytesWritten: 2048,
                success: true,
                isPending: false,
            }),
        });
        expect(container.querySelector('.write-result-text')?.textContent).toContain('Wrote');
        expect(container.querySelector('.write-result-path')?.textContent).toBe('src/file.go');
    });
});

describe('ToolCall - tool name display', () => {
    it('shows tool display name', () => {
        const { container } = render(ToolCall, {
            ...baseProps,
            toolCall: makeToolCall({
                toolName: 'write_file',
                args: { path: 'src/file.go' },
                success: true,
                isPending: false,
            }),
        });
        const toolName = container.querySelector('.tool-name');
        expect(toolName?.textContent).toBeTruthy();
    });
});

describe('ToolCall - duration display', () => {
    it('shows duration for completed tool calls', () => {
        const { container } = render(ToolCall, {
            ...baseProps,
            toolCall: makeToolCall({
                toolName: 'bash',
                args: { command: 'ls' },
                result: 'output',
                duration: 1500,
                success: true,
                isPending: false,
            }),
        });
        expect(container.querySelector('.tool-duration')).toBeTruthy();
    });
});

