import { describe, it, expect, vi, beforeAll, beforeEach, afterEach } from 'vitest';
import { render } from '@testing-library/svelte';

// Mock svelte transitions and raw asset imports (not available in jsdom)
vi.mock('svelte/transition', () => ({
    slide: vi.fn(),
    fade: vi.fn(),
}));
vi.mock('$lib/assets/icons/commit-push.svg?raw', () => ({ default: '' }));
vi.mock('$lib/components/Icon.svelte', () => ({ default: () => '' }));

// Card reads Svelte contexts normally created by App; provide standalone defaults
// The matchMedia polyfill for useIsMobile: tests that need the ⋯-menu shape flip `mobileMatches`
// before rendering.
let mobileMatches = false;
beforeAll(() => {
    vi.stubGlobal('matchMedia', (query: string) => ({
        matches: mobileMatches,
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
        projects: [],
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
        moved_at: new Date().toISOString(),
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
    const idleVar = (style.match(/--idle:\s*(\d+)/) || [])[1];
    return {
        container,
        elements,
        classes: elements.map(el => el.className.replace(/svelte-\w+/g, '').trim()),
        actionsVar,
        idleVar,
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
        expect(result.idleVar).toBe('1');
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

    it('counts create + delete for a done subtask in the done column', () => {
        // A done task keeps the + like every other card, so the padding counts two.
        const result = renderCard({
            task: makeTask({ status: 'done', execution_state: 'completed' }),
            columnId: 'done',
            inline: true,
        });
        expect(result.classes.some(c => c.includes('action-delete'))).toBe(true);
        expect(result.classes.some(c => c.includes('action-add'))).toBe(true);
        expect(result.elements.length).toBe(2);
        expect(result.actionsVar).toBe('2');
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

describe('Card action count on a narrow (mobile) viewport', () => {
    beforeEach(() => { mobileMatches = true; });
    afterEach(() => { mobileMatches = false; });

    it('counts the overflow menu alone for an inline subtask (not create + edit + move)', () => {
        const result = renderCard({
            task: makeTask({ status: 'backlog' }),
            columnId: 'backlog',
            inline: true,
        });
        expect(result.classes).toEqual(['action-icon action-overflow']);
        expect(result.elements.length).toBe(1);
        expect(result.actionsVar).toBe('1');
    });

    it('draws no Idle word on a narrow viewport: an idle subtask is the overflow menu alone', () => {
        const result = renderCard({
            task: makeTask({ status: 'in_progress', execution_state: 'idle' }),
            columnId: 'in_progress',
            inline: true,
        });
        expect(result.hasIdle).toBe(false);
        expect(result.classes).toEqual(['action-icon action-overflow']);
        expect(result.elements.length).toBe(1);
        expect(result.actionsVar).toBe('1');
        expect(result.idleVar).toBe('0');
    });

    it('leaves the third slot empty for an idle top-level card, rather than gaining a button', () => {
        const result = renderCard({
            task: makeTask({ id: 'top-1', parent_task_id: '', status: 'in_progress', execution_state: 'idle' }),
            columnId: 'in_progress',
            inline: false,
        });
        expect(result.hasIdle).toBe(false);
        expect(result.classes).toEqual(['action-icon action-add', 'action-icon']);
        expect(result.elements.length).toBe(2);
        expect(result.actionsVar).toBe('2');
        expect(result.idleVar).toBe('0');
    });

    it('still counts every button for a top-level card, which has no overflow menu', () => {
        const result = renderCard({
            task: makeTask({ id: 'top-1', parent_task_id: '', status: 'in_progress', execution_state: 'paused' }),
            columnId: 'in_progress',
            inline: false,
        });
        expect(result.elements.length).toBe(3);
        expect(result.actionsVar).toBe('3');
    });

    it('does not claim an Idle slot for a card that draws no Idle badge', () => {
        const result = renderCard({
            task: makeTask({ id: 'top-1', parent_task_id: '', status: 'in_progress', execution_state: '' }),
            columnId: 'in_progress',
            inline: false,
        });
        expect(result.hasIdle).toBe(false);
        expect(result.idleVar).toBe('0');
    });
});
