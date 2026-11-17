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

/** One card, and what the padding calc is told about it. `count` is the number of buttons drawn and
 *  `--actions` the reserve the title pads itself with; the two agreeing is the whole contract. */
interface Row {
    name: string;
    task?: Partial<Task>;
    columnId: string;
    inline?: boolean;
    showSubtaskBorder?: boolean;
    multiSelect?: unknown;
    /** Whether the card has a subtask to draw actions for. */
    children?: boolean;
    count: number;
    includes?: string[];
    excludes?: string[];
    /** The full class list, for the rows whose shape is the finding. */
    classes?: string[];
    hasIdle?: boolean;
    idleVar?: string;
}

function childOf(id: string, parentId: string): Task {
    return makeTask({ id, parent_task_id: parentId });
}

function checkRow(row: Row) {
    const result = renderCard({
        task: makeTask(row.task),
        columnId: row.columnId,
        childrenMap: row.children ? new Map([['sub-1', [childOf('gc-1', 'sub-1')]]]) : undefined,
        inline: row.inline,
        showSubtaskBorder: row.showSubtaskBorder,
        multiSelect: row.multiSelect,
    });

    expect(result.elements.length, row.name).toBe(row.count);
    expect(result.actionsVar, `${row.name}: the reserve counts every button drawn`).toBe(String(row.count));
    for (const cls of row.includes ?? []) expect(result.classes.some(c => c.includes(cls)), `${row.name}: ${cls}`).toBe(true);
    for (const cls of row.excludes ?? []) expect(result.classes.some(c => c.includes(cls)), `${row.name}: ${cls}`).toBe(false);
    if (row.classes) expect(result.classes, row.name).toEqual(row.classes);
    if (row.hasIdle !== undefined) expect(result.hasIdle, row.name).toBe(row.hasIdle);
    if (row.idleVar !== undefined) expect(result.idleVar, row.name).toBe(row.idleVar);
}

describe('Card action count (title padding calc)', () => {
    it.each([
        {
            name: 'an in-progress idle subtask counts the Idle badge',
            task: { status: 'in_progress', execution_state: 'idle' },
            columnId: 'in_progress', inline: true, children: true,
            count: 3, hasIdle: true, idleVar: '1', includes: ['action-add'],
        },
        {
            name: 'a running subtask counts the pause button',
            task: { status: 'in_progress', execution_state: 'running' },
            columnId: 'in_progress', inline: true, children: true,
            count: 3, includes: ['action-icon'],
        },
        {
            name: 'a stopped subtask counts the resume button',
            task: { status: 'in_progress', execution_state: 'stopped' },
            columnId: 'in_progress', inline: true, children: true,
            count: 3,
        },
        {
            // A done task keeps the + like every other card, so the padding counts two.
            name: 'a done subtask in the done column',
            task: { status: 'done', execution_state: 'completed' },
            columnId: 'done', inline: true,
            count: 2, includes: ['action-delete', 'action-add'],
        },
        {
            name: 'a backlog subtask with children has no third action',
            task: { status: 'backlog' },
            columnId: 'backlog', inline: true, children: true,
            count: 2,
        },
        {
            name: 'an in-progress idle top-level card counts the Idle badge',
            task: { id: 'top-1', parent_task_id: '', status: 'in_progress', execution_state: 'idle' },
            columnId: 'in_progress',
            count: 3, hasIdle: true,
        },
        {
            name: 'a backlog top-level card',
            task: { id: 'top-1', parent_task_id: '', status: 'backlog' },
            columnId: 'backlog',
            count: 3, includes: ['action-move'],
        },
        {
            name: 'a done top-level card in multi-select swaps delete for move',
            task: { id: 'top-1', parent_task_id: '', status: 'done', execution_state: 'completed' },
            columnId: 'done', multiSelect: { isSelected: () => true },
            count: 3, includes: ['action-move'], excludes: ['action-delete'],
        },
        {
            name: 'a sidebar subtask row shows the create-subtask button',
            task: { status: 'backlog' },
            columnId: 'backlog', showSubtaskBorder: true,
            count: 3, includes: ['action-add'],
        },
    ])('counts the buttons of $name', checkRow);
});

describe('Card action count on a narrow (mobile) viewport', () => {
    beforeEach(() => { mobileMatches = true; });
    afterEach(() => { mobileMatches = false; });

    it.each([
        {
            name: 'an inline subtask is the overflow menu alone',
            task: { status: 'backlog' },
            columnId: 'backlog', inline: true,
            count: 1, classes: ['action-icon action-overflow'],
        },
        {
            name: 'an idle subtask draws no Idle word beside the overflow menu',
            task: { status: 'in_progress', execution_state: 'idle' },
            columnId: 'in_progress', inline: true,
            count: 1, classes: ['action-icon action-overflow'], hasIdle: false, idleVar: '0',
        },
        {
            name: 'an idle top-level card leaves the third slot empty rather than gaining a button',
            task: { id: 'top-1', parent_task_id: '', status: 'in_progress', execution_state: 'idle' },
            columnId: 'in_progress',
            count: 2, classes: ['action-icon action-add', 'action-icon'], hasIdle: false, idleVar: '0',
        },
        {
            name: 'a top-level card still counts every button, having no overflow menu',
            task: { id: 'top-1', parent_task_id: '', status: 'in_progress', execution_state: 'paused' },
            columnId: 'in_progress',
            count: 3,
        },
        {
            name: 'a card that draws no Idle badge claims no Idle slot',
            task: { id: 'top-1', parent_task_id: '', status: 'in_progress', execution_state: '' },
            columnId: 'in_progress',
            count: 2, hasIdle: false, idleVar: '0',
        },
    ])('counts the buttons of $name', checkRow);
});
