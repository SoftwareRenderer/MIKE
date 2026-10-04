import { describe, it, expect, vi } from 'vitest';
import { render } from '@testing-library/svelte';

// Mock all sub-components that ChatEntries depends on.
vi.mock('$lib/components/Chat/ChatMessage.svelte', () => ({
    default: () => '<div class="mock-chat-message"></div>',
}));
vi.mock('$lib/components/Chat/CheckpointDivider.svelte', () => ({
    default: () => '<div class="mock-checkpoint-divider"></div>',
}));
vi.mock('$lib/components/Chat/QueueIndicator.svelte', () => ({
    default: () => '<div class="mock-queue-indicator"></div>',
}));
vi.mock('$lib/utils/toolArgs', () => ({
}));
vi.mock('$lib/constants', () => ({
    SCROLL_BOTTOM_THRESHOLD: 100,
}));

import ChatEntries from '$lib/components/Chat/ChatEntries.svelte';
import { createChatFollow } from '$lib/composables/use-chat-follow.svelte';

// Svelte 5 effects (incl. the follow-tracker attach) flush on the
// microtask queue, so give them a tick after render.
async function flush() {
    for (let i = 0; i < 6; i++) await Promise.resolve();
}

function makeEntry(id: string, role: 'user' | 'assistant') {
    return {
        id,
        role,
        content: `content of ${id}`,
        isStreaming: false,
        createdAt: new Date().toISOString(),
    };
}

function makeMeta() {
    return { cpState: null, cpThread: null, isLastUserMessage: false, isFirstUserMessage: false };
}

const baseProps = {
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
};

describe('ChatEntries — direct (non-virtualized) rendering', () => {
    it('renders every entry in the DOM (no virtualization windowing)', async () => {
        const entries = Array.from({ length: 15 }, (_, i) => makeEntry(`msg-${i}`, 'assistant'));
        const { container } = render(ChatEntries, {
            ...baseProps,
            entries,
            entryMetas: entries.map(makeMeta),
            lastEntryIdx: entries.length - 1,
            follow: createChatFollow(),
        });
        await flush();

        // Every entry is wrapped in its own .chat-entry — all present even though
        // the viewport would only "show" a few. A virtualizer would render far
        // fewer; here the DOM holds the full list (content-visibility handles
        // the off-screen paint skipping, not the JS).
        const entryEls = container.querySelectorAll('.chat-entry');
        expect(entryEls.length).toBe(15);

        // A single shared scroll container holds them all.
        expect(container.querySelectorAll('.chat-messages').length).toBe(1);
    });

    it('attaches the real scroll container to the shared follow tracker', async () => {
        const follow = createChatFollow();
        const entries = [makeEntry('msg-0', 'assistant')];
        const { container } = render(ChatEntries, {
            ...baseProps,
            entries,
            entryMetas: entries.map(makeMeta),
            lastEntryIdx: 0,
            follow,
        });
        await flush();

        // The tracker must be handed the real element the entries render into.
        expect(follow.el).toBeInstanceOf(HTMLElement);
        expect(container.contains(follow.el)).toBe(true);
        expect(follow.el?.classList.contains('chat-messages')).toBe(true);
    });
});
