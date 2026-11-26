import { afterEach, beforeEach, describe, it, expect, vi } from 'vitest';
import { render } from '@testing-library/svelte';

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

// The go-to-first control draws the conversation's opening page while the middle is still being read, and
// that gap is said in the list — only ever while rows are actually missing.

let realRO: unknown;
beforeEach(() => {
    realRO = (globalThis as { ResizeObserver?: unknown }).ResizeObserver;
    (globalThis as { ResizeObserver?: unknown }).ResizeObserver = class {
        observe() {} unobserve() {} disconnect() {}
    };
});
afterEach(() => {
    (globalThis as { ResizeObserver?: unknown }).ResizeObserver = realRO;
});

async function flush() {
    for (let i = 0; i < 6; i++) await Promise.resolve();
    await new Promise(r => requestAnimationFrame(r));
}

function entry(id: string, role: 'user' | 'assistant', content: string) {
    return { id, role, content, isStreaming: false, createdAt: new Date().toISOString() };
}

const meta = { cpState: null, cpThread: null, isLastUserMessage: false, isFirstUserMessage: false };

const baseProps = {
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
    projectId: 'p',
    taskId: 't',
    onDismissError: vi.fn(),
    onThreadSwitch: vi.fn(),
    onMsgDelete: vi.fn(),
    onMsgFork: vi.fn(),
    onMsgSend: vi.fn(),
    onRegenerate: vi.fn(),
    onNavigateTask: vi.fn(),
    hasEarlier: true,
    loadingEarlier: false,
    onLoadEarlier: () => Promise.resolve(),
};

// Mid-fill the list is the opening page and then, after a gap nobody can measure, the rows the window
// delivered. `w-1` is the first of those, so the notice belongs above it.
const opening = [entry('h-1', 'user', 'the first ask'), entry('h-2', 'assistant', 'its answer')];
const newest = [entry('w-1', 'user', 'the newest ask'), entry('w-2', 'assistant', 'its answer')];

function draw(gapAboveEntryId: string) {
    const entries = [...opening, ...newest];
    const follow = createChatFollow();
    const { container } = render(ChatEntries, {
        ...baseProps, entries, entryMetas: entries.map(() => ({ ...meta })), follow, gapAboveEntryId,
    });
    return { container, follow };
}

function notice(container: HTMLElement) {
    return container.querySelector('.chat-messages .history-notice');
}

describe('the gap between the opening page and the window', () => {
    it('names the rows that are missing, above the first row the window delivered', async () => {
        const { container } = draw('w-1');
        await flush();

        const band = notice(container);
        expect(band, 'the reader is looking at a transcript with rows missing and is told nothing').not.toBeNull();
        expect(band?.textContent).toContain('Loading messages');

        const rows = Array.from(container.querySelectorAll<HTMLElement>('.chat-messages .chat-entry[data-entry-id]'));
        expect(rows[0].dataset.entryId, 'the notice pushed the opening page out of the list').toBe('h-1');
        const noticeIndex = Array.from(container.querySelector('.chat-content')!.children).indexOf(band as Element);
        const firstWindowIndex = rows.findIndex(r => r.dataset.entryId === 'w-1');
        expect(noticeIndex, 'the notice sits below the rows that are missing, not above them').toBe(firstWindowIndex);
    });

    it('is quiet when the transcript is contiguous', async () => {
        const { container } = draw('');
        await flush();

        expect(notice(container), 'a contiguous transcript claimed rows were missing').toBeNull();
    });

    it('draws each row once, notice and all', async () => {
        const { container } = draw('w-1');
        await flush();

        const ids = Array.from(container.querySelectorAll<HTMLElement>('.chat-messages .chat-entry[data-entry-id]'))
            .map(n => n.dataset.entryId as string);
        expect(ids).toEqual(['h-1', 'h-2', 'w-1', 'w-2']);
    });
});
