/**
 * Where a checkpoint band is allowed to sit: above the user message whose arrival found the work in that
 * state. Nothing sits under the newest answer — its state is what the Changes panel already answers — and the
 * band that does draw has to say whether the Changes panel is showing it, because clicking it again is what
 * closes the panel.
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
const meta = (cpState: CheckpointState | null = null) =>
    ({ cpState, cpThread: null, isLastUserMessage: false, isFirstUserMessage: false });
const saved = (commitHash: string): CheckpointState => ({ isPending: false, commitHash });

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

/** Two turns, each with a saved checkpoint: only the first has a message after it, so only it has a band. */
async function renderTwoTurns(options: { openCheckpointHash?: string } = {}) {
    const entries = [entry('u1', 'user'), entry('a1', 'assistant'), entry('u2', 'user'), entry('a2', 'assistant')];
    const entryMetas = [meta(), meta(), meta(saved('c1')), meta()];
    const { container } = render(ChatEntries, {
        ...baseProps,
        entries,
        entryMetas,
        lastEntryIdx: entries.length - 1,
        follow: createChatFollow(),
        ...options,
    });
    await flush();
    return container;
}

describe('where a checkpoint band is drawn', () => {
    it('draws one band per turn, above the message that follows the turn that captured it', async () => {
        const container = await renderTwoTurns();

        expect(container.querySelectorAll('.checkpoint-divider').length).toBe(1);
        expect(container.querySelector('.chat-entry[data-entry-id="u2"] .checkpoint-divider')).not.toBeNull();
    });

    it('draws nothing below the last row, where the newest turn has no band to wear', async () => {
        const container = await renderTwoTurns();

        // A band beside the entries rather than inside one is the line back under the newest answer.
        expect(container.querySelectorAll('.chat-content > .checkpoint-divider').length).toBe(0);
        const rows = [...container.querySelectorAll('.chat-entry')];
        expect(rows[rows.length - 1]?.querySelector('.checkpoint-divider')).toBeNull();
    });

    it('reads as the open checkpoint while the Changes panel shows it', async () => {
        const closed = await renderTwoTurns();
        expect(closed.querySelector('.view-changes-btn')?.getAttribute('aria-expanded')).toBe('false');

        const open = await renderTwoTurns({ openCheckpointHash: 'c1' });
        expect(open.querySelector('.view-changes-btn')?.getAttribute('aria-expanded')).toBe('true');
    });
});
