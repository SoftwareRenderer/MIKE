/**
 * A checkpoint band is the only way a reader reaches the files a turn left behind, so the newest turn's band
 * has to be in the DOM after the list — and its button has to say whether the Changes panel is showing that
 * checkpoint, because clicking that button again is what closes the panel.
 */
import { describe, it, expect, vi } from 'vitest';
import { render } from '@testing-library/svelte';

vi.mock('$lib/components/Chat/ChatMessage.svelte', () => ({
    default: () => '<div class="mock-chat-message"></div>',
}));
vi.mock('$lib/components/Chat/QueueIndicator.svelte', () => ({
    default: () => '<div class="mock-queue-indicator"></div>',
}));
vi.mock('$lib/constants', () => ({
    SCROLL_BOTTOM_THRESHOLD: 100,
}));

import ChatEntries from '$lib/components/Chat/ChatEntries.svelte';
import { createChatFollow } from '$lib/composables/use-chat-follow.svelte';
import type { CheckpointState } from '$lib/utils/chat-entries';

// Svelte 5 effects flush on the microtask queue, so give them a tick after render.
async function flush() {
    for (let i = 0; i < 6; i++) await Promise.resolve();
}

const entry = (id: string, role: 'user' | 'assistant') => ({
    id, role, content: `content of ${id}`, isStreaming: false, createdAt: new Date().toISOString(),
});
const meta = () => ({ cpState: null, cpThread: null, isLastUserMessage: false, isFirstUserMessage: false });
const saved: CheckpointState = { isPending: false, commitHash: 'abc123' };

const baseProps = {
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
    onViewChanges: vi.fn(),
};

/** The turn order a reader sees: one question, one answer, nothing after it. */
async function renderTrailing(options: { trailingCheckpoint?: CheckpointState | null; openCheckpointHash?: string } = {}) {
    const entries = [entry('u1', 'user'), entry('a1', 'assistant')];
    const { container } = render(ChatEntries, {
        ...baseProps,
        entries,
        entryMetas: entries.map(meta),
        lastEntryIdx: entries.length - 1,
        follow: createChatFollow(),
        ...options,
    });
    await flush();
    return container;
}

describe('the trailing checkpoint band', () => {
    it('sits after the last message, where the newest turn has nothing else to carry it', async () => {
        const container = await renderTrailing({ trailingCheckpoint: saved });

        const bands = container.querySelectorAll('.checkpoint-divider');
        expect(bands.length).toBe(1);

        const rows = [...container.querySelectorAll('.chat-entry, .checkpoint-divider')];
        expect(rows[rows.length - 1]?.classList.contains('checkpoint-divider')).toBe(true);
    });

    it('answers nothing when the newest turn saved no state', async () => {
        const container = await renderTrailing({ trailingCheckpoint: null });
        expect(container.querySelectorAll('.checkpoint-divider').length).toBe(0);
    });

    it('reads as the open checkpoint while the Changes panel shows it', async () => {
        const closed = await renderTrailing({ trailingCheckpoint: saved });
        expect(closed.querySelector('.view-changes-btn')?.getAttribute('aria-expanded')).toBe('false');

        const open = await renderTrailing({ trailingCheckpoint: saved, openCheckpointHash: 'abc123' });
        expect(open.querySelector('.view-changes-btn')?.getAttribute('aria-expanded')).toBe('true');
    });
});
