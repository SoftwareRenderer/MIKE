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
vi.mock('$lib/constants', () => ({ SCROLL_BOTTOM_THRESHOLD: 100 }));

import ChatEntries from '$lib/components/Chat/ChatEntries.svelte';
import { createChatFollow } from '$lib/composables/use-chat-follow.svelte';
import { StreamState } from '$lib/services/stream-state.svelte';
import { createStreamHandler, type StreamHandlerCallbacks } from '$lib/services/stream-handler';
import type { StreamEvent } from '$lib/types';

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

    // The state while a turn's tool call runs: the entry still streams, its reasoning was never sent, its
    // count is closed. Only a count says thinking is arriving, so neither half alone raises the notice.
    it('says nothing about thinking while the tool call it asked for is running', async () => {
        const container = renderAssistant({
            isStreaming: true,
            hasReasoning: true,
            reasoningContent: '[HIDDEN]',
            toolCalls: [{ toolId: 'tc-1', toolName: 'bash', args: { command: 'ls' }, isPending: true, startedAt: Date.now() }],
        });
        await flush();

        expect(container.querySelector('.reasoning-pending')).toBeNull();
        expect(container.querySelector('.reasoning-toggle')).toBeTruthy();
    });
});

// The frames, not the block, decide whether a count still describes a run — and a stop is where they can
// disagree, leaving the ring turning over a task nobody is running.
describe('what the frames leave on screen once the run is stopped', () => {
    function streamCallbacks(): StreamHandlerCallbacks {
        return {
            genId: (() => { let n = 0; return () => `id${++n}xxxxx`; })(),
            scroll: vi.fn(),
            markStreaming: vi.fn(),
            markNotStreaming: vi.fn(),
            onRefreshDiff: vi.fn(),
            onThreadSwitch: vi.fn(),
            loadHistory: vi.fn(),
            updateActiveThread: vi.fn(),
            updateThreadUrl: vi.fn(),
            // The frames these drives stream arrive during a run.
            runInFlight: () => true,
        };
    }

    async function renderFrames(frames: StreamEvent[]) {
        const stream = new StreamState();
        const handleEvent = createStreamHandler(stream, streamCallbacks());
        for (const frame of frames) handleEvent(frame, false);
        const { container } = render(ChatEntries, {
            ...baseProps,
            entries: stream.state.entries,
            entryMetas: meta.map((m, i) => ({ ...m, isLastUserMessage: i === stream.state.entries.length - 1 })),
            lastEntryIdx: Math.max(0, stream.state.entries.length - 1),
            follow: createChatFollow(),
        });
        await flush();
        return container;
    }

    const count = (chars: number): StreamEvent => ({ type: 'reasoning_progress', data: { chars } });
    const status = (state: string, activity?: string): StreamEvent => ({ type: 'status', data: { state, activity } });

    it('turns nothing after the stop that ended the run', async () => {
        const container = await renderFrames([count(400), count(900), status('paused', 'Task paused by user')]);

        expect(container.querySelector('.reasoning-pending')).toBeNull();
        expect(container.querySelector('.reasoning-toggle')).toBeNull();
    });

    it('still says it is thinking while that same run runs', async () => {
        const container = await renderFrames([count(900)]);

        expect(container.querySelector('.reasoning-pending')?.textContent).toContain('Processing');
    });

    // The cancelled call flushes what it buffered after the transition, and that late count is not a run.
    it('is not repainted by the late count of the run that ended', async () => {
        const container = await renderFrames([status('paused'), count(1200)]);

        expect(container.querySelector('.reasoning-pending')).toBeNull();
    });
});
