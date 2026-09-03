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

// Svelte 5 effects flush on the microtask queue; the mount effect also queues
// a requestAnimationFrame scroll pin — drain both so they can't mutate
// scrollTop after the test set it.
async function flush() {
    for (let i = 0; i < 6; i++) await Promise.resolve();
    await new Promise((r) => requestAnimationFrame(r));
    await new Promise((r) => requestAnimationFrame(r));
}

function makeEntry(id: string, role: 'user' | 'assistant', content: string) {
    return { id, role, content, isStreaming: false, createdAt: new Date().toISOString() };
}

function makeMeta() {
    return { cpState: null, cpThread: null, checkpointIndex: -1, isLastUserMessage: false };
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

// Both user messages deliberately share the same first line, so the bubble
// text alone cannot tell them apart — only the click target can.
const entries = [
    makeEntry('u-a', 'user', 'Fix the bug\nplease'),
    makeEntry('a-1', 'assistant', 'On it'),
    makeEntry('u-b', 'user', 'Fix the bug\ntoo'),
    makeEntry('a-2', 'assistant', 'Done'),
];

// Ground-truth rendered geometry in content space (px from content top):
//   user A [0, 400)  ·  assistant [400, 500)  ·  user B [500, 600)  ·  assistant [600, 700)
// Note: A's real height (400px) is far from any naive text-length estimate.
const A_TOP = 0, A_BOTTOM = 400, B_TOP = 500, B_BOTTOM = 600;
const CONTAINER_TOP = 100;

function setRect(el: Element, top: number, bottom: number) {
    Object.defineProperty(el, 'getBoundingClientRect', {
        configurable: true,
        value: () => ({ top, bottom, left: 0, right: 0, width: 0, height: bottom - top, x: 0, y: top, toJSON: () => ({}) }),
    });
}

/** Position the container at scrollTop and give the rendered elements the
 *  geometry that geometry implies (viewport top at CONTAINER_TOP). */
function scrollTo(container: HTMLElement, scrollTop: number) {
    const el = container.querySelector<HTMLElement>('.chat-messages')!;
    setRect(el, CONTAINER_TOP, CONTAINER_TOP + 500);
    el.scrollTop = scrollTop;
    const [a, b] = el.querySelectorAll<HTMLElement>('.sticky-user-message');
    setRect(a, CONTAINER_TOP + A_TOP - scrollTop, CONTAINER_TOP + A_BOTTOM - scrollTop);
    setRect(b, CONTAINER_TOP + B_TOP - scrollTop, CONTAINER_TOP + B_BOTTOM - scrollTop);
    el.dispatchEvent(new Event('scroll'));
}

function stubScrollIntoView(container: HTMLElement): Map<HTMLElement, ReturnType<typeof vi.fn>> {
    const map = new Map<HTMLElement, ReturnType<typeof vi.fn>>();
    container.querySelectorAll<HTMLElement>('.chat-entry').forEach((el) => {
        const fn = vi.fn();
        Object.defineProperty(el, 'scrollIntoView', { configurable: true, value: fn });
        map.set(el, fn);
    });
    return map;
}

function entryEls(container: HTMLElement): HTMLElement[] {
    return Array.from(container.querySelectorAll<HTMLElement>('.chat-entry'));
}

async function renderChat() {
    const { container } = render(ChatEntries, {
        ...baseProps,
        entries,
        entryMetas: entries.map(makeMeta),
        lastEntryIdx: entries.length - 1,
        follow: createChatFollow(),
    });
    await flush();
    return container;
}

describe('ChatEntries — sticky user-message header', () => {
    it('hides the header while the first user message is in view', async () => {
        const container = await renderChat();
        scrollTo(container, 0);
        await flush();
        expect(container.querySelector('.chat-sticky-header')).toBeNull();
    });

    it('shows the user message that is actually above the viewport (measured, not estimated)', async () => {
        const container = await renderChat();
        // scrollTop 450: A (bottom 400) is fully above the fold; B (top 500)
        // is not. An estimate of A's height would put B far above the fold and
        // show the wrong message.
        scrollTo(container, 450);
        await flush();
        const bubble = container.querySelector<HTMLElement>('.chat-sticky-bubble');
        expect(bubble).toBeTruthy();
        expect(bubble?.textContent).toBe('Fix the bug');

        const spies = stubScrollIntoView(container);
        const [e0, , e2] = entryEls(container);
        bubble?.click();
        await flush();
        // The header must refer to the FIRST user message (entry 0), not the
        // later duplicate (entry 2).
        expect(spies.get(e0)).toHaveBeenCalledWith({ behavior: 'smooth', block: 'start' });
        expect(spies.get(e2)).not.toHaveBeenCalled();
    });

    it('follows the newest user message above the viewport and click targets it', async () => {
        const container = await renderChat();
        // scrollTop 550: B (top 500) has crossed the viewport top → B is latest.
        scrollTo(container, 550);
        await flush();
        const bubble = container.querySelector<HTMLElement>('.chat-sticky-bubble');
        expect(bubble).toBeTruthy();
        expect(bubble?.textContent).toBe('Fix the bug');

        const spies = stubScrollIntoView(container);
        const [e0, , e2] = entryEls(container);
        bubble?.click();
        await flush();
        expect(spies.get(e2)).toHaveBeenCalledWith({ behavior: 'smooth', block: 'start' });
        expect(spies.get(e0)).not.toHaveBeenCalled();
    });
});
