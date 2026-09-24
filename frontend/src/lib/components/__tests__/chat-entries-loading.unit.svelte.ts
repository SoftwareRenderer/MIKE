import { describe, it, expect, vi } from 'vitest';
import { render } from '@testing-library/svelte';

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
vi.mock('$lib/constants', () => ({
    SCROLL_BOTTOM_THRESHOLD: 100,
}));

import ChatEntries from '$lib/components/Chat/ChatEntries.svelte';
import { createChatFollow } from '$lib/composables/use-chat-follow.svelte';

const baseProps = {
    entries: [],
    entryMetas: [],
    lastEntryIdx: -1,
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
    taskStatus: 'backlog',
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

describe('ChatEntries — history loading indicator', () => {
    it('shows loading indicator when historyLoading is true and entries are empty', () => {
        const { container } = render(ChatEntries, {
            ...baseProps,
            entries: [],
            historyLoading: true,
            follow: createChatFollow(),
        });

        const wrapper = container.querySelector('.chat-messages-wrapper');
        expect(wrapper).toBeTruthy();

        // The loading indicator should be visible
        const loadingIndicator = container.querySelector('.chat-loading');
        expect(loadingIndicator).toBeTruthy();
        expect(loadingIndicator?.textContent).toContain('Loading conversation');
    });

    it('shows "No messages yet" empty state when historyLoading is false and entries are empty', () => {
        const { container } = render(ChatEntries, {
            ...baseProps,
            entries: [],
            historyLoading: false,
            follow: createChatFollow(),
        });

        // The empty state should be visible
        const emptyState = container.querySelector('.chat-empty');
        expect(emptyState).toBeTruthy();
        expect(emptyState?.textContent).toContain('No messages yet');

        // Loading indicator should NOT be present
        const loadingIndicator = container.querySelector('.chat-loading');
        expect(loadingIndicator).toBeFalsy();
    });

    it('does not show loading indicator when historyLoading is true but entries exist', () => {
        const { container } = render(ChatEntries, {
            ...baseProps,
            entries: [
                {
                    id: 'msg-1',
                    role: 'user',
                    content: 'Hello',
                    isStreaming: false,
                    createdAt: new Date().toISOString(),
                },
            ],
            entryMetas: [{ cpState: null, cpThread: null, checkpointIndex: -1, isLastUserMessage: false, isFirstUserMessage: false }],
            lastEntryIdx: 0,
            historyLoading: true,
            follow: createChatFollow(),
        });

        // Loading indicator should NOT be present when entries exist
        const loadingIndicator = container.querySelector('.chat-loading');
        expect(loadingIndicator).toBeFalsy();

        // Empty state should NOT be present either
        const emptyState = container.querySelector('.chat-empty');
        expect(emptyState).toBeFalsy();
    });

    it('loading indicator contains a spinner element', () => {
        const { container } = render(ChatEntries, {
            ...baseProps,
            entries: [],
            historyLoading: true,
            follow: createChatFollow(),
        });

        const loadingIndicator = container.querySelector('.chat-loading');
        const spinner = loadingIndicator?.querySelector('.spinner');
        expect(spinner).toBeTruthy();
    });
});
