/**
 * THE BOARD CARD'S OUT-OF-MEMORY PILL
 *
 * The pill matches the task view’s alert and vanishes when dismissed.
 */
import { describe, it, expect, vi, beforeAll, afterEach } from 'vitest';
import { render, cleanup } from '@testing-library/svelte';
import { writable } from 'svelte/store';

vi.mock('svelte/transition', () => ({ slide: vi.fn(), fade: vi.fn() }));
vi.mock('$lib/assets/icons/commit-push.svg?raw', () => ({ default: '' }));
vi.mock('$lib/components/Icon.svelte', () => ({ default: () => '' }));

vi.mock('$lib/context', () => ({
    getStreamingContext: () => writable(new Set<string>()),
    getTaskDiffStatsContext: () => writable({}),
    getCommitQueueContext: () => writable({}),
    getExpandedTasksContext: () => writable(new Set<string>()),
    getTaskEventContext: () => writable(null),
}));

const appStore = vi.hoisted(() => ({
    current: {
        selectedTask: null,
        board: null as { tasks: unknown[] } | null,
        config: { providers: [] },
        projects: [] as Array<{ id: string; sandbox_memory_limit?: number }>,
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

const task = (over: Partial<Task> = {}): Task => ({
    id: 'card-1',
    project_id: 'test-project',
    title: 'A task that needed more',
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
    sandbox_memory_peak_mb: 1024,
    ...over,
} as Task);

function renderCard(t: Task): HTMLElement {
    const { container } = render(Card, {
        task: t,
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

function projectWithLimit(limitMB: number) {
    appStore.current.projects = [{ id: 'test-project', sandbox_memory_limit: limitMB }];
}

describe("the card's out-of-memory pill", () => {
    afterEach(() => {
        appStore.current.projects = [];
        cleanup();
    });

    it('marks the card whose run reached the project limit', async () => {
        projectWithLimit(1024);
        const container = renderCard(task());
        expect(container.querySelector('.oom-badge')?.textContent).toContain('Out of memory');
    });

    it('says on hover what the run cost', async () => {
        projectWithLimit(1024);
        const container = renderCard(task({ sandbox_oom_kills: 2 }));
        expect((container.querySelector('.oom-badge') as HTMLElement).title)
            .toContain('2 processes were killed');
    });

    it('says nothing once the limit is raised above that peak', async () => {
        projectWithLimit(2048);
        const container = renderCard(task());
        expect(container.querySelector('.oom-badge')).toBeNull();
    });

    it('says nothing about a run that stayed under its limit', async () => {
        projectWithLimit(1024);
        const container = renderCard(task({ sandbox_memory_peak_mb: 512 }));
        expect(container.querySelector('.oom-badge')).toBeNull();
    });

    it('says nothing about the alert someone dismissed', async () => {
        projectWithLimit(1024);
        const container = renderCard(task({ memory_alert_ack: { peak_mb: 1024, oom_kills: 0, limit_mb: 1024 } }));
        expect(container.querySelector('.oom-badge')).toBeNull();
    });

    it('says it again when the container came back over what was dismissed', async () => {
        projectWithLimit(1024);
        const acked = task({ memory_alert_ack: { peak_mb: 1024, oom_kills: 1, limit_mb: 1024 } });
        expect(renderCard(acked).querySelector('.oom-badge')).toBeNull();
        // One more kill than the dismissal covers is a worse run.
        const container = renderCard({ ...acked, sandbox_oom_kills: 2 });
        expect(container.querySelector('.oom-badge')).not.toBeNull();
    });
});
