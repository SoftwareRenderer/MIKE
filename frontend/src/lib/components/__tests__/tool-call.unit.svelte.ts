import { describe, it, expect, vi } from 'vitest';
import { render, fireEvent } from '@testing-library/svelte';
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
// NOTE: this file used to mock `marked` and `dompurify` directly. ToolCall no longer
// imports either — markdown goes through `$lib/utils/sanitize` — so those mocks were
// both dead and misleading: an identity `sanitize` mock means this suite could never
// have noticed a sink that stopped sanitizing. What the three markdown sinks render is
// pinned in `chat-sink-sanitization.unit.svelte.ts`, which mocks nothing of the sort.
vi.mock('$lib/utils/diff-parser', () => ({
    buildModifiedSegments: () => ({ oldSegments: [], newSegments: [] }),
}));
vi.mock('$lib/api', () => ({
    getTaskMessageFull: vi.fn(),
}));

vi.mock('$lib/types', () => ({
    getToolDisplayName: (displayName: string | undefined, toolName: string) => displayName || toolName.replace('_', ' '),
}));

// `$lib/utils/toolArgs` is deliberately unmocked: the progress line reads the same streamed path
// the header prints, and a `formatToolArgs` stub that ignored the partial arguments would let this
// suite disagree with the app about whether the filename is already on screen.

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
    // Which of the three sources names the path is `streamedPath`'s own contract, pinned beside its
    // definition in `toolArgs.unit.ts`; what this card decides is what it does with the answer.
    it('names the file a pending edit_file is editing', () => {
        const { container } = render(ToolCall, {
            ...baseProps,
            toolCall: makeToolCall({
                toolName: 'edit_file',
                args: { path: 'src/file.go', edits: [{ oldText: 'a', newText: 'b' }] },
                isPending: true,
            }),
        });
        expect(container.querySelector('.progress-text')?.textContent).toBe('Editing: src/file.go');
    });

    it('names the edited file the tool event carries when the arguments do not', () => {
        const { container } = render(ToolCall, {
            ...baseProps,
            toolCall: makeToolCall({
                toolName: 'edit_file',
                args: {},
                path: 'src/file.go',
                isPending: true,
            }),
        });
        expect(container.querySelector('.tool-args')?.textContent).toBe('');
        expect(container.querySelector('.progress-text')?.textContent).toBe('Editing: src/file.go');
    });

    it('shows "Editing..." for a pending edit_file while no source names a file', () => {
        const { container } = render(ToolCall, {
            ...baseProps,
            toolCall: makeToolCall({
                toolName: 'edit_file',
                args: {},
                raw_args: '{"edits":[{"oldText":"a',
                isPending: true,
            }),
        });
        expect(container.querySelector('.progress-text')?.textContent).toBe('Editing...');
    });

    it('shows "Writing..." for a pending write_file with no filename yet', () => {
        const { container } = render(ToolCall, {
            ...baseProps,
            toolCall: makeToolCall({
                toolName: 'write_file',
                args: {},
                isPending: true,
            }),
        });
        expect(container.querySelector('.progress-text')?.textContent).toBe('Writing...');
    });

    it('names the file in the header alone while its content is still generating', () => {
        const { container } = render(ToolCall, {
            ...baseProps,
            toolCall: makeToolCall({
                toolName: 'write_file',
                args: {},
                raw_args: '{"path":"src/file.go","content":"package ma',
                isPending: true,
            }),
        });
        expect(container.querySelector('.tool-args')?.textContent).toBe('./src/file.go');
        expect(container.querySelector('.progress-text')?.textContent).toBe('Writing...');
    });

    it('names the file from the tool event when the arguments do not carry it yet', () => {
        const { container } = render(ToolCall, {
            ...baseProps,
            toolCall: makeToolCall({
                toolName: 'write_file',
                args: {},
                raw_args: '{"content":"package ma',
                path: 'src/file.go',
                isPending: true,
            }),
        });
        expect(container.querySelector('.tool-args')?.textContent).toBe('');
        expect(container.querySelector('.progress-text')?.textContent).toBe('Writing: src/file.go');
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
                args: { path: 'src/file.go', content: 'package main' },
                isPending: true,
                startedAt,
            }),
        });
        const text = container.querySelector('.progress-text')?.textContent;
        expect(text).toContain('Running for');
    });

    it('holds the elapsed time back while the write is still generating', () => {
        const startedAt = Date.now() - 10000;
        const { container } = render(ToolCall, {
            ...baseProps,
            toolCall: makeToolCall({
                toolName: 'write_file',
                args: {},
                raw_args: '{"path":"src/file.go","content":"package ma',
                isPending: true,
                startedAt,
            }),
        });
        expect(container.querySelector('.progress-text')?.textContent).toBe('Writing...');
    });

    it('holds the elapsed time back, and the filename with it, while the edit is still generating', () => {
        const startedAt = Date.now() - 10000;
        const { container } = render(ToolCall, {
            ...baseProps,
            toolCall: makeToolCall({
                toolName: 'edit_file',
                args: {},
                raw_args: '{"path":"src/file.go","edits":[{"oldText":"a',
                isPending: true,
                startedAt,
            }),
        });
        expect(container.querySelector('.progress-text')?.textContent).toBe('Editing: src/file.go');
    });

    it('shows elapsed time for an edit whose arguments have arrived and is now running', () => {
        const startedAt = Date.now() - 10000;
        const { container } = render(ToolCall, {
            ...baseProps,
            toolCall: makeToolCall({
                toolName: 'edit_file',
                args: {},
                raw_args: '{"path":"src/file.go","edits":[{"oldText":"a","newText":"b"}]}',
                isPending: true,
                startedAt,
            }),
        });
        expect(container.querySelector('.progress-text')?.textContent).toContain('Running for');
    });

    it('shows elapsed time for an edit handed over as parsed arguments with no raw body', () => {
        // An agent adapter's frame carries args and no `raw_args`, so the arguments being whole is
        // the only signal that this call is executing rather than still generating.
        const startedAt = Date.now() - 10000;
        const { container } = render(ToolCall, {
            ...baseProps,
            toolCall: makeToolCall({
                toolName: 'edit_file',
                args: { path: 'src/file.go', edits: [{ oldText: 'a', newText: 'b' }] },
                isPending: true,
                startedAt,
            }),
        });
        expect(container.querySelector('.progress-text')?.textContent).toContain('Running for');
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

    it('renders without throwing when an edit is missing oldText', () => {
        // Model/adapter args can arrive partial or malformed — the diff must
        // degrade to an insertion instead of crashing the chat render.
        const { container } = render(ToolCall, {
            ...baseProps,
            toolCall: makeToolCall({
                toolName: 'edit_file',
                args: { path: 'src/file.go', edits: [{ newText: 'brand new line' }] },
                success: true,
                isPending: false,
            }),
        });
        expect(container.querySelector('.edit-diff-toggle')).toBeTruthy();
        // Degrades to an insertion pair (buildModifiedSegments is mocked here,
        // so assert on structure, not text) instead of throwing.
        expect(container.querySelectorAll('.edit-diff-line').length).toBe(2);
        expect(container.querySelector('.edit-diff-line.edit-added')).toBeTruthy();
    });

    it('hides the diff toggle when no edit is renderable', () => {
        const { container } = render(ToolCall, {
            ...baseProps,
            toolCall: makeToolCall({
                toolName: 'edit_file',
                args: { path: 'src/file.go', edits: [{ old_text: 'a', new_text: 'b' }, null] },
                success: true,
                isPending: false,
            }),
        });
        expect(container.querySelector('.edit-diff-toggle')).toBeFalsy();
        expect(container.querySelector('.edit-diff')).toBeFalsy();
    });
});

describe('ToolCall - edit diff expands and collapses', () => {
    // The card is controlled: its expanded state is `editDiffOverrides[toolId]`, and a card nobody has
    // clicked opens expanded only while the change is trivial (<= 2 changed lines). Both halves are what a
    // reader calls "the diff never opened" and "the click did nothing", and `file-editor.spec.ts` drives
    // the same two clicks through a live turn.
    const trivialEdit = { path: 'src/file.go', edits: [{ oldText: 'a\nb', newText: 'a\nb\nnew line' }] };
    const bigEdit = { path: 'src/file.go', edits: [{ oldText: 'a\nb\nc', newText: 'x\ny\nz' }] };

    function renderCard(args: Record<string, unknown>, overrides: Record<string, boolean> = {}) {
        const onToggleEditDiff = vi.fn();
        const { container } = render(ToolCall, {
            ...baseProps,
            editDiffOverrides: overrides,
            onToggleEditDiff,
            toolCall: makeToolCall({ toolName: 'edit_file', args, success: true, isPending: false }),
        });
        return { container, toggle: container.querySelector('.edit-diff-toggle')!, onToggleEditDiff };
    }

    it('opens a trivial change expanded and asks the parent to collapse it', async () => {
        const { toggle, onToggleEditDiff } = renderCard(trivialEdit);
        expect(toggle.getAttribute('aria-expanded')).toBe('true');

        await fireEvent.click(toggle);
        expect(onToggleEditDiff).toHaveBeenCalledWith('tool-1', true);
    });

    it('hides the diff once the parent reports the card collapsed, and asks for it back', async () => {
        const { container, onToggleEditDiff } = renderCard(trivialEdit, { 'tool-1': false });
        const collapsed = container.querySelector('.edit-diff-toggle')!;
        expect(collapsed.getAttribute('aria-expanded')).toBe('false');
        expect(container.querySelector('.edit-diff')).toBeFalsy();

        await fireEvent.click(collapsed);
        expect(onToggleEditDiff).toHaveBeenCalledWith('tool-1', false);
    });

    it('leaves a change bigger than two lines collapsed until it is clicked', async () => {
        const { container, toggle, onToggleEditDiff } = renderCard(bigEdit);
        expect(toggle.getAttribute('aria-expanded')).toBe('false');
        expect(container.querySelector('.edit-diff')).toBeFalsy();

        await fireEvent.click(toggle);
        expect(onToggleEditDiff).toHaveBeenCalledWith('tool-1', false);
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
        expect(container.querySelector('.write-result-text')?.textContent).toBe('Wrote 2.0 KB to:');
        expect(container.querySelector('.write-result-path')?.textContent).toBe('src/file.go');
    });

    it('offers the written file as a jump into the task explorer', async () => {
        const received: unknown[] = [];
        const listener = (e: Event) => received.push((e as CustomEvent).detail);
        window.addEventListener('open-file', listener);
        try {
            const { container } = render(ToolCall, {
                ...baseProps,
                taskId: 'frost-amber-quartz',
                toolCall: makeToolCall({
                    toolName: 'write_file',
                    args: { path: 'src/file.go' },
                    path: 'src/file.go',
                    bytesWritten: 2048,
                    success: true,
                    isPending: false,
                }),
            });
            const link = container.querySelector('button.write-result-path');
            expect(link?.textContent).toBe('src/file.go');
            await fireEvent.click(link as HTMLElement);
            expect(received).toEqual([{ path: 'src/file.go', taskId: 'frost-amber-quartz' }]);
        } finally {
            window.removeEventListener('open-file', listener);
        }
    });

    it('takes the path from the tool arguments when the result carries none', () => {
        const { container } = render(ToolCall, {
            ...baseProps,
            taskId: 'frost-amber-quartz',
            toolCall: makeToolCall({
                toolName: 'write_file',
                args: { path: 'hello.bas' },
                bytesWritten: 82,
                success: true,
                isPending: false,
            }),
        });
        expect(container.querySelector('button.write-result-path')?.textContent).toBe('hello.bas');
    });

    it('is plain text with no task to open the file in', () => {
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
        expect(container.querySelector('button.write-result-path')).toBeFalsy();
        expect(container.querySelector('span.write-result-path')?.textContent).toBe('src/file.go');
    });

    it('is plain text when the write named no file', () => {
        const { container } = render(ToolCall, {
            ...baseProps,
            taskId: 'frost-amber-quartz',
            toolCall: makeToolCall({
                toolName: 'write_file',
                args: {},
                bytesWritten: 12,
                success: true,
                isPending: false,
            }),
        });
        expect(container.querySelector('button.write-result-path')).toBeFalsy();
        expect(container.querySelector('span.write-result-path')?.textContent).toBe('(unknown)');
    });

    it('is not a jump while the write is still streaming', () => {
        const { container } = render(ToolCall, {
            ...baseProps,
            taskId: 'frost-amber-quartz',
            toolCall: makeToolCall({
                toolName: 'write_file',
                args: { path: 'src/file.go' },
                path: 'src/file.go',
                isPending: true,
            }),
        });
        expect(container.querySelector('.write-result-path')).toBeFalsy();
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

