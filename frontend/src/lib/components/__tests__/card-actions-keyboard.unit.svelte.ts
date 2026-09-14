import { describe, it, expect, vi, beforeAll, beforeEach, afterEach } from 'vitest';
import { render, cleanup, fireEvent } from '@testing-library/svelte';

// Mocks needed by Card (same shape as card-action-padding.unit.svelte.ts).
vi.mock('svelte/transition', () => ({ slide: vi.fn(), fade: vi.fn() }));
vi.mock('$lib/assets/icons/commit-push.svg?raw', () => ({ default: '' }));
vi.mock('$lib/components/Icon.svelte', () => ({ default: () => '' }));

vi.mock('$lib/context', () => {
    function writable(initial: unknown) {
        return {
            subscribe: (fn: (v: unknown) => void) => { fn(initial); return () => {}; },
            set: () => {},
            update: () => {},
        };
    }
    return {
        getStreamingContext: () => writable(new Set<string>()),
        getTaskDiffStatsContext: () => writable({}),
        getCommitQueueContext: () => writable({}),
        getExpandedTasksContext: () => writable(new Set<string>()),
        getTaskEventContext: () => writable(null),
    };
});

const { mockAppStore, mockUiStore } = vi.hoisted(() => ({
    mockAppStore: {
        selectedTask: null as unknown,
        board: null,
        projects: [],
        config: { providers: [] } as Record<string, unknown>,
        editTaskData: null,
        showEditTask: false,
        selectedProject: null,
    },
    mockUiStore: { showCardDetail: false },
}));

vi.mock('$lib/stores', () => ({
    appStore: mockAppStore,
    uiStore: mockUiStore,
    dialogStore: { showToast: vi.fn(), showConfirm: vi.fn() },
    updateTaskInBoard: vi.fn(),
    removeTaskFromBoard: vi.fn(),
}));

import Card from '$lib/components/Card.svelte';
import type { Task } from '$lib/types';

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

beforeEach(() => {
    mockAppStore.selectedTask = null;
    mockAppStore.showEditTask = false;
    mockAppStore.editTaskData = null;
    mockUiStore.showCardDetail = false;
});

afterEach(() => cleanup());

function makeTask(id: string, overrides: Partial<Task> = {}): Task {
    return {
        id,
        project_id: 'proj-1',
        title: `Task ${id}`,
        prompt: 'do it',
        status: 'backlog',
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

function renderCard(props: {
    task: Task;
    childrenMap?: Map<string, Task[]>;
    expandedTasks?: Set<string>;
    onSelect?: (t: Task) => void;
} = { task: makeTask('card-1') }) {
    const { container } = render(Card, {
        task: props.task,
        columnId: props.task.status,
        projectId: 'proj-1',
        childrenMap: props.childrenMap ?? new Map<string, Task[]>(),
        expandedTasks: props.expandedTasks ?? new Set<string>(),
        onToggleExpanded: vi.fn(),
        onCreateChildTask: vi.fn(),
        columnColor: '#888888',
        inline: false,
        onSelect: props.onSelect,
        parentSelected: false,
        showSubtaskBorder: false,
        onRefreshBoard: vi.fn(),
        multiSelect: null,
    });
    return container;
}

function opened(): boolean {
    return mockAppStore.selectedTask !== null || mockUiStore.showCardDetail;
}

describe('Card keyboard activation reaches the control that has focus', () => {
    it("Enter on an action icon is the icon's activation, not the card's", async () => {
        const container = renderCard();
        const edit = container.querySelector<HTMLElement>('.action-icon[aria-label="Edit task"]')!;
        edit.focus();

        const prevented = (await fireEvent.keyDown(edit, { key: 'Enter', bubbles: true })) === false;

        expect(prevented, 'preventDefault on the icon keydown cancels the button activation').toBe(false);
        expect(opened()).toBe(false);
        expect(mockAppStore.showEditTask, 'the icon action must not have run from the keydown alone').toBe(false);
    });

    it('Space on an action icon is not swallowed by the card', async () => {
        const container = renderCard();
        const move = container.querySelector<HTMLElement>('.action-icon.action-move')!;
        move.focus();

        const prevented = (await fireEvent.keyDown(move, { key: ' ', bubbles: true })) === false;

        expect(prevented).toBe(false);
        expect(opened()).toBe(false);
    });

    it('Enter on the subtask expand bar does not open the parent task', async () => {
        const container = renderCard({
            task: makeTask('parent-1'),
            childrenMap: new Map([['parent-1', [makeTask('child-1', { parent_task_id: 'parent-1' })]]]),
        });
        const bar = container.querySelector<HTMLElement>('.card > .subtask-progress-bar')!;
        bar.focus();

        const prevented = (await fireEvent.keyDown(bar, { key: 'Enter', bubbles: true })) === false;

        expect(prevented).toBe(false);
        expect(opened()).toBe(false);
    });

    it('Enter and Space on the card itself open the task', async () => {
        const container = renderCard();
        const card = container.querySelector<HTMLElement>('.card')!;
        card.focus();

        await fireEvent.keyDown(card, { key: 'Enter', bubbles: true });
        expect(mockUiStore.showCardDetail).toBe(true);
        expect((mockAppStore.selectedTask as Task | null)?.id).toBe('card-1');

        mockUiStore.showCardDetail = false;
        mockAppStore.selectedTask = null;
        await fireEvent.keyDown(card, { key: ' ', bubbles: true });

        expect(mockUiStore.showCardDetail).toBe(true);
        expect((mockAppStore.selectedTask as Task | null)?.id).toBe('card-1');
    });

    it('Enter on a subtask card selects the subtask, not the parent it sits in', async () => {
        const child = makeTask('child-1', { parent_task_id: 'parent-1' });
        const container = renderCard({
            task: makeTask('parent-1'),
            childrenMap: new Map([['parent-1', [child]]]),
            expandedTasks: new Set(['parent-1']),
        });
        const childCard = container.querySelector<HTMLElement>('.subtask-wrapper .card')!;
        expect(childCard.dataset.taskId).toBe('child-1');
        childCard.focus();

        await fireEvent.keyDown(childCard, { key: 'Enter', bubbles: true });

        expect((mockAppStore.selectedTask as Task | null)?.id).toBe('child-1');
    });

    it("activating an action icon with the pointer runs its own action, not the card's", async () => {
        const container = renderCard();
        const edit = container.querySelector<HTMLElement>('.action-icon[aria-label="Edit task"]')!;

        await fireEvent.click(edit, { bubbles: true });

        expect(mockAppStore.showEditTask).toBe(true);
        expect(opened()).toBe(false);
    });
});
