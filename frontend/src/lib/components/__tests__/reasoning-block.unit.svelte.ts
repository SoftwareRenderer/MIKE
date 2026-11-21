import { describe, it, expect, afterEach, vi } from 'vitest';
import { render, cleanup, fireEvent, waitFor } from '@testing-library/svelte';
import ReasoningBlock from '../Chat/ReasoningBlock.svelte';
import { getTaskMessageFull } from '$lib/api';

// The one way a withheld row's words arrive is this read, so the click that opens it is when the bandwidth
// saved is paid back.
vi.mock('$lib/api', () => ({
    getTaskMessageFull: vi.fn(async () => ({ reasoning_content: 'What it actually thought' })),
}));

afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
});

function renderBlock(props: Record<string, unknown> = {}) {
    const utils = render(ReasoningBlock, {
        props: {
            reasoningContent: '',
            hasReasoning: false,
            isStreaming: false,
            content: '',
            toolCallsLength: 0,
            ...props,
        },
    });
    return {
        container: utils.container,
        pending: () => utils.container.querySelector('.reasoning-pending'),
        pendingText: () => utils.container.querySelector('.reasoning-pending')?.textContent ?? '',
        spinner: () => utils.container.querySelector('.reasoning-pending .spinner'),
        toggle: () => utils.container.querySelector('button.reasoning-toggle'),
        text: () => utils.container.querySelector('.reasoning-content'),
    };
}

describe('a collapsed turn waiting on its reasoning', () => {
    const pending = { chars: 4000, startedAt: Date.now() - 2000 };

    it('says it is processing and at what rate, without a word of the reasoning', () => {
        const block = renderBlock({ hasReasoning: true, isStreaming: true, reasoningPending: pending });

        expect(block.pending()).toBeTruthy();
        expect(block.pendingText()).toContain('Processing');
        // The figure is `formatReasoningRate`'s business; this corner owes a rate being there at all.
        expect(block.pendingText()).toMatch(/\d+(\.\d+)? tok\/s/);
        expect(block.text()).toBeNull();
    });

    it('spins a ring and announces the wait once, so a screen reader is not left reading tokens per second', () => {
        const block = renderBlock({ hasReasoning: true, isStreaming: true, reasoningPending: pending });

        expect(block.spinner()).toBeTruthy();
        expect(block.pending()?.getAttribute('role')).toBe('status');
        expect(block.spinner()?.getAttribute('aria-hidden')).toBe('true');
    });

    it('offers nothing to open while there is nothing stored to open', () => {
        const block = renderBlock({ hasReasoning: true, isStreaming: true, reasoningPending: pending });

        expect(block.toggle()).toBeNull();
    });

    it('hands the corner back to the toggle once the call has stopped streaming', () => {
        const block = renderBlock({ hasReasoning: true, isStreaming: false, reasoningPending: pending });

        expect(block.pending()).toBeNull();
        expect(block.toggle()).toBeTruthy();
    });

    it('asks the server for the words only when the reader opens a withheld row', async () => {
        const onReasoningResolved = vi.fn();
        const block = renderBlock({
            hasReasoning: true,
            isStreaming: false,
            messageId: 'row-1',
            projectId: 'proj-1',
            taskId: 'task-1',
            reasoningPending: pending,
            onReasoningResolved,
        });

        expect(getTaskMessageFull).not.toHaveBeenCalled();
        await fireEvent.click(block.toggle()!);

        await waitFor(() => expect(onReasoningResolved).toHaveBeenCalledWith('What it actually thought'));
        expect(getTaskMessageFull).toHaveBeenCalledWith('proj-1', 'task-1', 'row-1');
    });

    it('shows the text the server did send instead of a rate', () => {
        const block = renderBlock({
            hasReasoning: true,
            isStreaming: true,
            reasoningContent: 'A short thought',
            reasoningPending: pending,
        });

        expect(block.pending()).toBeNull();
        expect(block.text()?.textContent).toBe('A short thought');
    });

    it('says nothing at all about a turn that reasoned about nothing', () => {
        const block = renderBlock({ isStreaming: true, reasoningPending: { chars: 0, startedAt: Date.now() } });

        expect(block.pending()).toBeNull();
        expect(block.toggle()).toBeNull();
    });
});
