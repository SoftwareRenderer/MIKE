import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, cleanup } from '@testing-library/svelte';

// Which branch draws an entry is layout, not a verdict on what the row can show: the collapsed turn's
// thinking notice arrives through the ordinary entry branch, and a branch that forgets the count leaves the
// reader a "Show reasoning" toggle over a call still thinking. The handler and the block were both tested;
// this is the wiring between them.

window.matchMedia = vi.fn().mockImplementation((query: string) => ({
    matches: false,
    media: query,
    onchange: null,
    addEventListener: vi.fn(),
    removeEventListener: vi.fn(),
    dispatchEvent: vi.fn(),
}));

vi.mock('$lib/api', () => ({ getTaskMessageFull: vi.fn(), uploadTaskFile: vi.fn() }));
vi.mock('$lib/utils/toolArgs', () => ({}));
vi.mock('$lib/constants', () => ({ SCROLL_BOTTOM_THRESHOLD: 100 }));

import ChatEntries from '$lib/components/Chat/ChatEntries.svelte';
import { createChatFollow } from '$lib/composables/use-chat-follow.svelte';

async function flush() {
    for (let i = 0; i < 6; i++) await Promise.resolve();
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

const meta = [{ cpState: null, cpThread: null, isLastUserMessage: false, isFirstUserMessage: false }];

function renderAssistant(entry: Record<string, unknown>) {
    const entries = [{ id: 'assistant-row', role: 'assistant', content: '', createdAt: new Date().toISOString(), ...entry }];
    const { container } = render(ChatEntries, {
        ...baseProps,
        entries,
        entryMetas: meta,
        lastEntryIdx: 0,
        follow: createChatFollow(),
    });
    return container;
}

afterEach(cleanup);

describe('a collapsed call as the ordinary entry branch draws it', () => {
    it('says it is thinking, at this rate, instead of offering the reasoning it cannot show', async () => {
        const container = renderAssistant({
            isStreaming: true,
            hasReasoning: true,
            reasoningPending: { chars: 800, startedAt: Date.now() - 2000 },
        });
        await flush();

        const pending = container.querySelector('.reasoning-pending');
        expect(pending?.textContent).toContain('Processing');
        expect(pending?.textContent).toContain('tok/s');
        expect(container.querySelector('.reasoning-toggle')).toBeNull();
    });

    it('offers the toggle once the call is over and the reasoning stays in the row', async () => {
        const container = renderAssistant({
            isStreaming: false,
            hasReasoning: true,
            reasoningContent: '[HIDDEN]',
        });
        await flush();

        expect(container.querySelector('.reasoning-pending')).toBeNull();
        expect(container.querySelector('.reasoning-toggle')).toBeTruthy();
    });
});
