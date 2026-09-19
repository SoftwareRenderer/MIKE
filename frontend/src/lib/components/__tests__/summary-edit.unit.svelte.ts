import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, cleanup, fireEvent } from '@testing-library/svelte';

// Which summary a user may rewrite, and what rewriting it does. The rule lives in two places: the
// offer (only the summary in effect carries an edit affordance) in ChatEntries, and the rewrite
// itself in ChatMessage, which saves where the entry stands instead of forking a thread or sending
// a message. ChatMessage is not stubbed here — a stub would let the affordance disappear from the
// entry that has the prop and still pass.

// jsdom has no matchMedia, and ChatMessage reads the viewport through useIsMobile.
window.matchMedia = vi.fn().mockImplementation((query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    dispatchEvent: vi.fn(),
}));

// The chat components reach the API layer transitively; nothing here makes a request.
vi.mock('$lib/api', () => ({ getTaskMessageFull: vi.fn(), uploadTaskFile: vi.fn() }));
vi.mock('$lib/constants', () => ({ SCROLL_BOTTOM_THRESHOLD: 100 }));

import ChatEntries from '$lib/components/Chat/ChatEntries.svelte';
import ChatMessage from '$lib/components/Chat/ChatMessage.svelte';
import { createChatFollow } from '$lib/composables/use-chat-follow.svelte';

const EDIT_SUMMARY = 'Edit the summarized context';

/** A spy whose signature the components' props accept. */
function summaryEditSpy() {
    return vi.fn(async (_id: string, _content: string) => {});
}

function editSummarySpy() {
    return vi.fn(async (_content: string) => {});
}

function entry(id: string, role: string, content: string) {
    return { id, role, content, isStreaming: false, createdAt: new Date().toISOString() };
}

const entriesBaseProps = {
    lastEntryIdx: 0,
    threads: [],
    awaitingResponse: false,
    queuePosition: 0,
    queueEstimatedWait: 0,
    promptProgress: 0,
    retryCount: 0,
    retryError: '',
    isAgentSelected: () => false,
    taskStatus: 'in_progress',
    taskExecutionState: '',
    projectId: 'p1',
    taskId: 't1',
    onDismissError: vi.fn(),
    onThreadSwitch: vi.fn(),
    onMsgDelete: vi.fn(),
    onMsgFork: vi.fn(),
    onMsgSend: vi.fn(),
    onRegenerate: vi.fn(),
    onNavigateTask: vi.fn(),
};

function renderEntries(entries: ReturnType<typeof entry>[], onSummaryEdit?: (id: string, content: string) => Promise<void>) {
    const metas = entries.map(() => ({ cpState: null, cpThread: null, checkpointIndex: -1, isLastUserMessage: false }));
    return render(ChatEntries, {
        props: {
            ...entriesBaseProps,
            entries,
            entryMetas: metas,
            lastEntryIdx: entries.length - 1,
            onSummaryEdit,
            follow: createChatFollow(),
        },
    });
}

afterEach(cleanup);

describe('the summarized context entry', () => {
    it('offers the edit on the newest summary only', () => {
        const { container } = renderEntries(
            [
                entry('u1', 'user', 'first request'),
                entry('sum-old', 'summarization', 'the superseded summary'),
                entry('a1', 'assistant', 'continuing'),
                entry('sum-new', 'summarization', 'the summary in effect'),
            ],
            summaryEditSpy(),
        );
        const blocks = [...container.querySelectorAll('.summarization-entry')];
        expect(blocks).toHaveLength(2);
        const editable = blocks.filter(b => b.querySelector(`button[aria-label="${EDIT_SUMMARY}"]`));
        expect(editable).toHaveLength(1);
        expect(editable[0].textContent).toContain('the summary in effect');
    });

    it('offers nothing on a compaction summary the backend never stored', () => {
        // Agent-mode compaction answers with the text alone, so the client appends the entry under a
        // generated 8-character id. There is no row for a PATCH to rewrite.
        const { container } = renderEntries([entry('a1b2c3d4', 'summarization', 'compacted by the agent')], summaryEditSpy());
        expect(container.querySelector(`button[aria-label="${EDIT_SUMMARY}"]`)).toBeNull();
    });

    it('offers nothing when the caller has no edit to make', () => {
        const { container } = renderEntries([entry('sum-1', 'summarization', 'the summary in effect')]);
        expect(container.querySelector(`button[aria-label="${EDIT_SUMMARY}"]`)).toBeNull();
    });
});

describe('editing the summarized context', () => {
    function renderSummary(onEditSummary: (content: string) => Promise<void>) {
        return render(ChatMessage, {
            props: { role: 'assistant', content: 'the summary as stored', messageId: 'sum-1', onEditSummary },
        });
    }

    it('saves the edited text through the callback, not as a message', async () => {
        const onEditSummary = editSummarySpy();
        const { container, getByDisplayValue } = renderSummary(onEditSummary);

        await fireEvent.click(container.querySelector(`button[aria-label="${EDIT_SUMMARY}"]`)!);
        const textarea = getByDisplayValue('the summary as stored') as HTMLTextAreaElement;
        textarea.value = 'the summary as corrected by hand';
        await fireEvent.input(textarea);

        const save = [...container.querySelectorAll('button')].find(b => b.textContent?.trim() === 'Save');
        expect(save).toBeTruthy();
        await fireEvent.click(save!);

        expect(onEditSummary).toHaveBeenCalledWith('the summary as corrected by hand');
    });

    it('leaves the text alone when the edit is cancelled', async () => {
        const onEditSummary = editSummarySpy();
        const { container, getByDisplayValue, queryByDisplayValue } = renderSummary(onEditSummary);

        await fireEvent.click(container.querySelector(`button[aria-label="${EDIT_SUMMARY}"]`)!);
        const textarea = getByDisplayValue('the summary as stored') as HTMLTextAreaElement;
        textarea.value = 'text nobody wanted';
        await fireEvent.input(textarea);
        await fireEvent.keyDown(textarea, { key: 'Escape' });

        expect(onEditSummary).not.toHaveBeenCalled();
        expect(queryByDisplayValue('text nobody wanted')).toBeNull();
        expect(container.textContent).toContain('the summary as stored');
    });

    it('does not save on a bare Enter, which writes a newline into prose', async () => {
        const onEditSummary = editSummarySpy();
        const { container, getByDisplayValue } = renderSummary(onEditSummary);

        await fireEvent.click(container.querySelector(`button[aria-label="${EDIT_SUMMARY}"]`)!);
        const textarea = getByDisplayValue('the summary as stored') as HTMLTextAreaElement;
        await fireEvent.keyDown(textarea, { key: 'Enter' });
        expect(onEditSummary).not.toHaveBeenCalled();

        await fireEvent.keyDown(textarea, { key: 'Enter', ctrlKey: true });
        expect(onEditSummary).toHaveBeenCalledWith('the summary as stored');
    });
});
