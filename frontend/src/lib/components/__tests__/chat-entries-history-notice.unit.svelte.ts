import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, fireEvent, cleanup } from '@testing-library/svelte';

// Which of the three screens the transcript region shows, and the phrase each one lands on, is what the
// reader depends on: the loading screen, the empty state, the rows, and the notice that names the gap
// between them.
vi.mock('$lib/components/Chat/ChatMessage.svelte', () => ({
    default: () => '<div class="mock-chat-message"></div>',
}));
vi.mock('$lib/components/Chat/CheckpointDivider.svelte', () => ({
    default: () => '<div class="mock-checkpoint-divider"></div>',
}));
vi.mock('$lib/components/Chat/QueueIndicator.svelte', () => ({
    default: () => '<div class="mock-queue-indicator"></div>',
}));
vi.mock('$lib/constants', () => ({
    SCROLL_BOTTOM_THRESHOLD: 100,
}));

import ChatEntries from '$lib/components/Chat/ChatEntries.svelte';
import { createChatFollow } from '$lib/composables/use-chat-follow.svelte';

/** A fresh follow tracker per render: one tracker attached by two components loops the effects. */
function props(over: Record<string, unknown> = {}) {
    return {
        follow: createChatFollow(),
        entries: [],
        entryMetas: [],
        lastEntryIdx: 0,
        activeThreadId: 'main',
        threads: [],
        awaitingResponse: false,
        connected: true,
        queuePosition: 0,
        queueEstimatedWait: 0,
        promptProgress: 0,
        retryCount: 0,
        retryError: '',
        isAgentSelected: () => false,
        taskStatus: 'in_progress',
        taskExecutionState: '',
        projectId: 'test-project',
        taskId: 'test-task',
        onDismissError: vi.fn(),
        onRetryConnect: vi.fn(),
        onThreadSwitch: vi.fn(),
        onMsgDelete: vi.fn(),
        onMsgFork: vi.fn(),
        onMsgSend: vi.fn(),
        onRegenerate: vi.fn(),
        onNavigateTask: vi.fn(),
        ...over,
    };
}

afterEach(cleanup);

function noticeLine(container: HTMLElement): HTMLElement | null {
    return container.querySelector('.history-notice');
}

/** The rows a pending paint floats over: an empty transcript gets the loading screen instead. */
function heldRows(n = 2) {
    const entries = Array.from({ length: n }, (_, i) => ({
        id: `row-${i}`, role: 'assistant', content: `answer ${i}`, isStreaming: false,
        createdAt: new Date().toISOString(),
    }));
    return {
        entries,
        entryMetas: entries.map(() => ({
            cpState: null, cpThread: null,
            isLastUserMessage: false, isFirstUserMessage: false,
        })),
        lastEntryIdx: n - 1,
    };
}

describe('ChatEntries — what the transcript says about itself', () => {
    // Which of the three screens the region shows, and what it says, is one decision: the read, the rows it
    // holds, and the notice that names the gap between them.
    it('labels the region and shows the loading screen only over an empty transcript', () => {
        const { container } = render(ChatEntries, props({ historyLoading: true }));
        expect(container.querySelector('.chat-messages')?.getAttribute('aria-label')).toBe('Chat messages');
        const loading = container.querySelector('.chat-loading');
        expect(loading?.querySelector('p')?.textContent).toBe('Loading conversation…');
        expect(loading?.querySelector('.spinner')).toBeTruthy();

        const held = render(ChatEntries, props({ historyLoading: true, ...heldRows() }));
        expect(held.container.querySelector('.chat-loading')).toBeNull();
        expect(held.container.querySelector('.empty-state')).toBeNull();
    });

    it('shows the empty state only when the read answered with nothing', () => {
        const { container } = render(ChatEntries, props({ historyLoading: false }));
        expect(container.querySelector('.empty-state .title')?.textContent).toBe('No messages yet');
        expect(container.querySelector('.empty-state .hint')?.textContent)
            .toBe('Send a message to start the conversation');
        expect(container.querySelector('.chat-loading')).toBeNull();
    });

    it('says the running turn is all it has when the server still owes this client', () => {
        const { container } = render(ChatEntries, props({ historyNotice: 'owing' }));
        const line = noticeLine(container);
        expect(line?.textContent).toContain('showing the running turn as it arrives');
        expect(line?.getAttribute('role')).toBe('status');
    });

    it('names the failure plainly when nothing else is on its way', () => {
        const { container } = render(ChatEntries, props({ historyNotice: 'failed' }));
        const line = noticeLine(container);
        expect(line?.textContent).toContain('Could not read this conversation');
        // The difference between the two is that content is still arriving, so that sentence must not
        // appear here.
        expect(line?.textContent).not.toContain('showing the running turn as it arrives');
    });

    it('says cached, not failed, when the mirror answered', () => {
        const { container } = render(ChatEntries, props({ historyNotice: 'cached' }));
        const line = noticeLine(container);
        expect(line?.textContent).toContain('Showing cached data');
        expect(line?.textContent).not.toContain('Could not read this conversation');
    });

    it('says nothing when the read answered', () => {
        const { container } = render(ChatEntries, props({ historyNotice: null }));
        expect(noticeLine(container)).toBeNull();
    });

    it('offers the reader the way out of the banner', async () => {
        const onRetryHistory = vi.fn();
        const { container } = render(ChatEntries, props({ historyNotice: 'failed', onRetryHistory }));
        await fireEvent.click(container.querySelector('.history-notice-retry') as Element);
        expect(onRetryHistory).toHaveBeenCalledTimes(1);
    });

    it('wears no fault vocabulary while a read is still out', () => {
        const { container } = render(ChatEntries, props({ historyNotice: 'pending', ...heldRows() }));
        expect(noticeLine(container)).toBeNull();
        // The fact is still said, just as a wait: the spinner carries the loading string as its name.
        const pending = container.querySelector('.chat-pending');
        expect(pending?.getAttribute('role')).toBe('status');
        expect(pending?.getAttribute('aria-label')).toContain('Loading conversation');
    });

    it('gives the band back when the read a pending paint floated over does not answer', () => {
        const { container } = render(ChatEntries, props({ historyNotice: 'failed', ...heldRows() }));
        expect(container.querySelector('.chat-pending')).toBeNull();
        expect(noticeLine(container)?.textContent).toContain('Could not read this conversation');
    });
});
