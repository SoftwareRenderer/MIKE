import { describe, it, expect, vi } from 'vitest';
import { render, fireEvent, cleanup, act } from '@testing-library/svelte';

// Mock all sub-components that ChatEntries depends on
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
// The follow tracker reads its bottom threshold from this module, so stub it here.
vi.mock('$lib/constants', () => ({
    SCROLL_BOTTOM_THRESHOLD: 100,
}));

import ChatEntries from '$lib/components/Chat/ChatEntries.svelte';
import { createChatFollow } from '$lib/composables/use-chat-follow.svelte';

async function flush() {
    for (let i = 0; i < 6; i++) await Promise.resolve();
}

/** Fake layout on the mounted container (jsdom has none). scrollTop clamps like a browser. */
function stubLayout(el: HTMLElement, { scrollHeight = 1000, clientHeight = 300, scrollTop = 0 } = {}) {
    const layout = { scrollHeight, clientHeight, scrollTop };
    for (const prop of ['scrollHeight', 'clientHeight'] as const) {
        Object.defineProperty(el, prop, { configurable: true, get: () => layout[prop] });
    }
    Object.defineProperty(el, 'scrollTop', {
        configurable: true,
        get: () => layout.scrollTop,
        set: (v: number) => { layout.scrollTop = Math.max(0, Math.min(v, layout.scrollHeight - layout.clientHeight)); },
    });
    return layout;
}

function makeEntries(n: number) {
    return Array.from({ length: n }, (_, i) => ({
        id: `msg-${i}`,
        role: 'assistant' as const,
        content: `content ${i}`,
        isStreaming: false,
        createdAt: new Date().toISOString(),
    }));
}

const baseProps = {
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
};

async function mount(count = 3) {
    const follow = createChatFollow();
    const entries = makeEntries(count);
    const { container, ...rest } = render(ChatEntries, {
        ...baseProps,
        entries,
        entryMetas: entries.map(() => ({ cpState: null, cpThread: null, checkpointIndex: -1, isLastUserMessage: false, isFirstUserMessage: false })),
        lastEntryIdx: entries.length - 1,
        follow,
    });
    await flush();
    const scroller = container.querySelector('.chat-messages') as HTMLElement;
    // A real user scroll: gesture first, then the scroll event it produces.
    const userScrollTo = async (top: number) => {
        stubLayout(scroller, { scrollHeight: 1000, clientHeight: 300, scrollTop: top });
        await fireEvent.wheel(scroller, { deltaY: -400 });
        await fireEvent.scroll(scroller);
        await flush();
    };
    return { follow, container, scroller, userScrollTo, ...rest };
}

describe('ChatEntries — Jump to latest button', () => {
    it('shows the button only while the tracker says the user left the bottom', async () => {
        const { follow, scroller, userScrollTo } = await mount();
        stubLayout(scroller, { scrollHeight: 1000, clientHeight: 300, scrollTop: 700 });

        // Following the bottom → no button.
        await fireEvent.scroll(scroller);
        expect(follow.atBottom).toBe(true);
        expect(scroller.closest('.chat-messages-wrapper')?.querySelector('.chat-jump-latest')).toBeNull();

        // User scrolls up past the threshold → the button appears.
        await userScrollTo(200);
        expect(follow.atBottom).toBe(false);
        const btn = scroller.closest('.chat-messages-wrapper')?.querySelector('.chat-jump-latest');
        expect(btn).toBeTruthy();
        expect(btn?.getAttribute('aria-label')).toMatch(/latest/i);
        expect(btn?.getAttribute('title')).toBeTruthy();

        // Back near the bottom → button goes away again.
        stubLayout(scroller, { scrollHeight: 1000, clientHeight: 300, scrollTop: 700 });
        await fireEvent.scroll(scroller);
        await flush();
        expect(scroller.closest('.chat-messages-wrapper')?.querySelector('.chat-jump-latest')).toBeNull();
        cleanup();
    });

    it('clicking the button forces a pin to the bottom', async () => {
        const { follow, scroller, userScrollTo } = await mount();
        stubLayout(scroller, { scrollHeight: 1000, clientHeight: 300, scrollTop: 700 });
        await fireEvent.scroll(scroller);
        await userScrollTo(100);
        expect(follow.atBottom).toBe(false);

        const btn = scroller.closest('.chat-messages-wrapper')?.querySelector('.chat-jump-latest') as HTMLElement;
        expect(btn).toBeTruthy();
        const pinSpy = vi.spyOn(follow, 'pin');
        await act(async () => { await fireEvent.click(btn); });

        expect(pinSpy).toHaveBeenCalledWith(true);
        // Clamped to the real bottom: 1000 - 300.
        expect(scroller.scrollTop).toBe(700);
        expect(follow.atBottom).toBe(true);
        cleanup();
    });

    it('does not render the button when there are no entries', async () => {
        const { container } = await mount(0);
        expect(container.querySelector('.chat-jump-latest')).toBeNull();
        cleanup();
    });

    it('routes scroll events through the shared tracker', async () => {
        const { follow, scroller } = await mount();
        const spy = vi.spyOn(follow, 'handleScroll');
        await fireEvent.scroll(scroller);
        expect(spy).toHaveBeenCalled();
        cleanup();
    });

    it('hands the real scroll container to the tracker', async () => {
        const { follow, scroller } = await mount();
        expect(follow.el).toBe(scroller);
        cleanup();
    });

    it('wraps the entries in a content element the tracker can observe', async () => {
        const { container } = await mount();
        const content = container.querySelector('.chat-content');
        expect(content).toBeTruthy();
        expect(container.querySelector('.chat-messages')?.contains(content ?? null)).toBe(true);
        expect(content?.querySelectorAll('.chat-entry').length).toBe(3);
        cleanup();
    });
});
