import { describe, it, expect, vi, beforeAll } from 'vitest';
import { render } from '@testing-library/svelte';
import { tick } from 'svelte';
import { writable } from 'svelte/store';

// ============================================================
// CARD STATUS BADGE — reactive sources
// ============================================================
// Two async inputs must both re-render the badge: the streamingIds context and the task's
// execution_state. get() on a store inside $derived.by does not create a dependency, so Card
// mirrors the store into $state with an $effect; a regression there leaves the card stuck on
// "Running" — invisible to a test that renders only once.

vi.mock('svelte/transition', () => ({ slide: vi.fn(), fade: vi.fn() }));
vi.mock('$lib/assets/icons/commit-push.svg?raw', () => ({ default: '' }));
vi.mock('$lib/components/Icon.svelte', () => ({ default: () => '' }));

const streamingStore = writable(new Set<string>());
const diffStatsStore = writable({});
const commitQueueStore = writable({});
const expandedTasksStore = writable(new Set<string>());
const taskEventStore = writable(null);

vi.mock('$lib/context', () => ({
    getStreamingContext: () => streamingStore,
    getTaskDiffStatsContext: () => diffStatsStore,
    getCommitQueueContext: () => commitQueueStore,
    getExpandedTasksContext: () => expandedTasksStore,
    getTaskEventContext: () => taskEventStore,
}));

const appStore = vi.hoisted(() => ({
    current: {
        selectedTask: null,
        board: null as { tasks: Array<{ id: string; execution_state?: string; has_merge_conflict?: boolean }> } | null,
        config: { providers: [] },
        editTaskData: null,
        showEditTask: false,
        selectedProject: null,
    },
}));

vi.mock('$lib/stores', () => ({
    appStore: appStore.current,
    uiStore: {},
    dialogStore: { showToast: vi.fn(), showConfirm: vi.fn() },
    updateTaskInBoard: vi.fn(),
    removeTaskFromBoard: vi.fn(),
}));

beforeAll(() => {
    vi.stubGlobal('matchMedia', (query: string) => ({
        matches: false,
        media: query,
        onchange: null,
        addEventListener: () => {},
        removeEventListener: () => {},
        addListener: () => {},
        removeListener: () => {},
        dispatchEvent: () => false,
    }));
});

import Card from '$lib/components/Card.svelte';
import type { Task } from '$lib/types';

function makeTask(overrides: Partial<Task> = {}): Task {
    return {
        id: 'card-1',
        project_id: 'test-project',
        title: 'A task that streams',
        prompt: 'do something',
        status: 'in_progress',
        execution_state: '',
        has_merge_conflict: false,
        max_consecutive_failed_turns: 0,
        auto_commit: true,
        auto_merge: true,
        subtasks_auto_merge: true,
        operating_mode: 'act',
        title_generating: false,
        summarizing: false,
        created_at: new Date().toISOString(),
        updated_at: new Date().toISOString(),
        moved_at: new Date().toISOString(),
        ...overrides,
    } as Task;
}

function badgeText(container: HTMLElement): string {
    return (container.querySelector('.badges-row .badge')?.textContent || '').trim();
}

function renderCard(task: Task) {
    const { container } = render(Card, {
        task,
        columnId: 'in_progress',
        projectId: 'test-project',
        childrenMap: new Map<string, Task[]>(),
        expandedTasks: new Set<string>(),
        onToggleExpanded: vi.fn(),
        onCreateChildTask: vi.fn(),
        columnColor: '#888888',
        inline: false,
        showSubtaskBorder: false,
        multiSelect: null as never,
        onSelect: vi.fn(),
        parentSelected: false,
        onRefreshBoard: vi.fn(),
    });
    return container as HTMLElement;
}

describe('Card status badge tracks its reactive sources', () => {
    it('shows Running while this client has an in-flight turn for the task', () => {
        streamingStore.set(new Set(['card-1']));
        try {
            const container = renderCard(makeTask());
            expect(badgeText(container)).toBe('Running');
        } finally {
            streamingStore.set(new Set());
        }
    });

    it('drops the Running badge when the streaming store is cleared (the $state mirror is live)', async () => {
        streamingStore.set(new Set(['card-1']));
        const container = renderCard(makeTask());
        expect(badgeText(container)).toBe('Running');

        // This is the whole point: a badge that read the store with get() would
        // keep showing Running after the turn ended.
        streamingStore.set(new Set());
        await tick();
        expect(badgeText(container)).not.toBe('Running');
    });

    it("does not mark a card Running for another task's stream", () => {
        streamingStore.set(new Set(['some-other-task']));
        try {
            const container = renderCard(makeTask({ execution_state: 'paused' }));
            expect(badgeText(container)).toBe('Paused');
        } finally {
            streamingStore.set(new Set());
        }
    });
});
