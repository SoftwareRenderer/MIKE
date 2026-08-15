import { describe, it, expect, vi, beforeAll } from 'vitest';
import { render } from '@testing-library/svelte';

// Mock svelte transitions and raw asset imports (not available in jsdom)
vi.mock('svelte/transition', () => ({
    slide: vi.fn(),
    fade: vi.fn(),
}));
vi.mock('$lib/assets/icons/commit-push.svg?raw', () => ({ default: '' }));
vi.mock('$lib/components/Icon.svelte', () => ({ default: () => '' }));

// Card reads Svelte contexts normally created by App; provide standalone defaults
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
        getTaskDiffContext: () => writable({}),
        getCommitQueueContext: () => writable({}),
        getExpandedTasksContext: () => writable(new Set<string>()),
        getTaskEventContext: () => writable(null),
    };
});

// Polyfill matchMedia for useIsMobile (desktop: never matches)
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

vi.mock('$lib/stores', () => ({
    appStore: {
        selectedTask: null,
        board: null,
        config: { providers: [] },
        editTaskData: null,
        showEditTask: false,
        selectedProject: null,
    },
    uiStore: {},
    dialogStore: { showToast: vi.fn(), showConfirm: vi.fn() },
    updateTaskInBoard: vi.fn(),
    removeTaskFromBoard: vi.fn(),
}));

import Card from '$lib/components/Card.svelte';
import type { Task } from '$lib/types';

function makeTask(overrides: Partial<Task> = {}): Task {
    return {
        id: 'sub-1',
        project_id: 'test-project',
        title: 'A task with a fairly long title that overflows',
        prompt: 'do something',
        status: 'backlog',
        column_order: 0,
        execution_state: '',
        has_merge_conflict: false,
        max_consecutive_failed_turns: 0,
        auto_commit: true,
        auto_merge: true,
        subtasks_auto_merge: true,
        operating_mode: 'act',
        title_generating: false,
        summarizing: false,
        parent_task_id: 'parent-1',
        created_at: new Date().toISOString(),
        updated_at: new Date().toISOString(),
        ...overrides,
    } as Task;
}

interface CardProps {
    task: Task;
    columnId: string;
    childrenMap?: Map<string, Task[]>;
    inline?: boolean;
    showSubtaskBorder?: boolean;
    multiSelect?: unknown;
}

function renderCard({ task, columnId, childrenMap, inline = false, showSubtaskBorder = false, multiSelect = null }: CardProps) {
    const { container } = render(Card, {
        task,
        columnId,
        projectId: 'test-project',
        childrenMap: childrenMap ?? new Map<string, Task[]>(),
        expandedTasks: new Set<string>(),
        onToggleExpanded: vi.fn(),
        onCreateChildTask: vi.fn(),
        columnColor: '#888888',
        inline,
        onSelect: vi.fn(),
        parentSelected: false,
        showSubtaskBorder,
        onRefreshBoard: vi.fn(),
        multiSelect: multiSelect as never,
    });
    const card = container.querySelector('.card')!;
    const actions = card.querySelector('.card-actions')!;
    const elements = [...actions.querySelectorAll('.action-icon, .action-idle')];
    const style = card.getAttribute('style') || '';
    const actionsVar = (style.match(/--actions:\s*(\d+)/) || [])[1];
    return {
        container,
        elements,
        classes: elements.map(el => el.className.replace(/svelte-\w+/g, '').trim()),
        actionsVar,
        hasIdle: elements.some(el => el.classList.contains('action-idle')),
    };
}

describe('Card action count (title padding calc)', () => {
    it('counts the Idle badge for an in-progress idle subtask (create + edit + idle)', () => {
        const child = makeTask({ id: 'gc-1', parent_task_id: 'sub-1' });
        const result = renderCard({
            task: makeTask({ status: 'in_progress', execution_state: 'idle' }),
            columnId: 'in_progress',
            childrenMap: new Map([['sub-1', [child]]]),
            inline: true,
        });
        expect(result.hasIdle).toBe(true);
        expect(result.classes.some(c => c.includes('action-add'))).toBe(true);
        expect(result.elements.length).toBe(3);
        expect(result.actionsVar).toBe('3');
    });

    it('counts the pause button for a running subtask (create + edit + pause)', () => {
        const child = makeTask({ id: 'gc-1', parent_task_id: 'sub-1' });
        const result = renderCard({
            task: makeTask({ status: 'in_progress', execution_state: 'running' }),
            columnId: 'in_progress',
            childrenMap: new Map([['sub-1', [child]]]),
            inline: true,
        });
        expect(result.elements.length).toBe(3);
        expect(result.classes.some(c => c.includes('action-icon'))).toBe(true);
        expect(result.actionsVar).toBe('3');
    });

    it('counts the resume button for a stopped subtask (create + edit + resume)', () => {
        const child = makeTask({ id: 'gc-1', parent_task_id: 'sub-1' });
        const result = renderCard({
            task: makeTask({ status: 'in_progress', execution_state: 'stopped' }),
            columnId: 'in_progress',
            childrenMap: new Map([['sub-1', [child]]]),
            inline: true,
        });
        expect(result.elements.length).toBe(3);
        expect(result.actionsVar).toBe('3');
    });

    it('counts the delete button for a done subtask in the done column (delete only)', () => {
        const result = renderCard({
            task: makeTask({ status: 'done', execution_state: 'completed' }),
            columnId: 'done',
            inline: true,
        });
        expect(result.classes.some(c => c.includes('action-delete'))).toBe(true);
        expect(result.elements.length).toBe(1);
        expect(result.actionsVar).toBe('1');
    });

    it('keeps create + edit for a backlog subtask with children (no third action)', () => {
        const child = makeTask({ id: 'gc-1', parent_task_id: 'sub-1' });
        const result = renderCard({
            task: makeTask({ status: 'backlog' }),
            columnId: 'backlog',
            childrenMap: new Map([['sub-1', [child]]]),
            inline: true,
        });
        expect(result.elements.length).toBe(2);
        expect(result.actionsVar).toBe('2');
    });

    it('counts the Idle badge for an in-progress idle top-level card (create + edit + idle)', () => {
        const result = renderCard({
            task: makeTask({ id: 'top-1', parent_task_id: '', status: 'in_progress', execution_state: 'idle' }),
            columnId: 'in_progress',
            inline: false,
        });
        expect(result.hasIdle).toBe(true);
        expect(result.elements.length).toBe(3);
        expect(result.actionsVar).toBe('3');
    });

    it('counts create + edit + move for a backlog top-level card', () => {
        const result = renderCard({
            task: makeTask({ id: 'top-1', parent_task_id: '', status: 'backlog' }),
            columnId: 'backlog',
            inline: false,
        });
        expect(result.classes.some(c => c.includes('action-move'))).toBe(true);
        expect(result.elements.length).toBe(3);
        expect(result.actionsVar).toBe('3');
    });

    it('counts create + edit + move (delete swapped for move) in multi-select on a done card', () => {
        const multiSelect = { isSelected: () => true };
        const result = renderCard({
            task: makeTask({ id: 'top-1', parent_task_id: '', status: 'done', execution_state: 'completed' }),
            columnId: 'done',
            inline: false,
            multiSelect,
        });
        expect(result.classes.some(c => c.includes('action-delete'))).toBe(false);
        expect(result.classes.some(c => c.includes('action-move'))).toBe(true);
        expect(result.elements.length).toBe(3);
        expect(result.actionsVar).toBe('3');
    });

    it('shows the create-subtask button and counts it for sidebar subtasks', () => {
        const result = renderCard({
            task: makeTask({ status: 'backlog' }),
            columnId: 'backlog',
            inline: false,
            showSubtaskBorder: true,
        });
        expect(result.classes.some(c => c.includes('action-add'))).toBe(true);
        expect(result.elements.length).toBe(3);
        expect(result.actionsVar).toBe('3');
    });
});
