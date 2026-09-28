import { describe, it, expect, vi, beforeAll, beforeEach, afterEach } from 'vitest';
import { render, cleanup, fireEvent } from '@testing-library/svelte';

// The flat subtask outline: one list of rows in run order, grouped by sequence, no card inside a card.

vi.mock('svelte/transition', () => ({ slide: vi.fn(), fade: vi.fn() }));
vi.mock('$lib/assets/icons/commit-push.svg?raw', () => ({ default: '' }));
vi.mock('$lib/components/Icon.svelte', () => ({ default: () => '' }));

const ONE = 'http://onemodel.local/v1';
const TWO = 'http://twomodel.local/v1';

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

/** Two providers so a badge has a provider name to print. */
function config(overrides: Record<string, unknown> = {}) {
    return {
        providers: [
            { id: 1, name: 'One', url: ONE, kind: 'llm', models: [{ name: 'alpha' }] },
            { id: 2, name: 'Two', url: TWO, kind: 'llm', models: [{ name: 'beta' }] },
        ],
        worker_provider: '',
        worker_model: '',
        ...overrides,
    };
}

beforeEach(() => {
    mockAppStore.selectedTask = null;
    mockAppStore.showEditTask = false;
    mockAppStore.editTaskData = null;
    mockAppStore.config = config();
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
        parent_task_id: '',
        created_at: new Date().toISOString(),
        updated_at: new Date().toISOString(),
        moved_at: new Date().toISOString(),
        ...overrides,
    } as Task;
}

/**
 * root → [a, b], a → [a1], a1 → [a1a]. Run order inside the `a` group is a1a, a1, a: a row is
 * listed after everything it waits for, because that is when `exec` starts it.
 */
function tree(statuses: Record<string, string> = {}) {
    const tasks = {
        root: makeTask('root', { title: 'Root' }),
        a: makeTask('a', { parent_task_id: 'root', title: 'Branch a' }),
        a1: makeTask('a1', { parent_task_id: 'a', title: 'Branch a1' }),
        a1a: makeTask('a1a', { parent_task_id: 'a1', title: 'Leaf a1a' }),
        b: makeTask('b', { parent_task_id: 'root', title: 'Branch b' }),
    };
    for (const [id, status] of Object.entries(statuses)) tasks[id as keyof typeof tasks].status = status;
    const childrenMap = new Map<string, Task[]>([
        ['root', [tasks.a, tasks.b]],
        ['a', [tasks.a1]],
        ['a1', [tasks.a1a]],
    ]);
    return { task: tasks.root, tasks, childrenMap, expanded: new Set(['root', 'a', 'a1']) };
}

function renderOutline(
    opts: { task: Task; childrenMap: Map<string, Task[]>; expanded: Set<string> },
    onToggleExpanded = vi.fn(),
) {
    const { container } = render(Card, {
        task: opts.task,
        columnId: opts.task.status,
        projectId: 'proj-1',
        childrenMap: opts.childrenMap,
        expandedTasks: opts.expanded,
        onToggleExpanded,
        onCreateChildTask: vi.fn(),
        columnColor: '#888888',
        inline: false,
        onSelect: vi.fn(),
        parentSelected: false,
        showSubtaskBorder: false,
        onRefreshBoard: vi.fn(),
        multiSelect: null as never,
    });
    return { container, onToggleExpanded };
}

/** The rows of the list, in the order the DOM holds them. */
function rowIds(container: HTMLElement): string[] {
    return [...container.querySelectorAll('.subtask-wrapper .card')].map(el => (el as HTMLElement).dataset.taskId!);
}

function rowWrapper(container: HTMLElement, id: string): HTMLElement {
    return [...container.querySelectorAll<HTMLElement>('.subtask-wrapper')]
        .find(w => w.querySelector(`.card[data-task-id="${id}"]`))!;
}

describe('Card subtask outline', () => {
    it('lists the whole subtree as sibling rows — no card inside a card', () => {
        const { container } = renderOutline(tree());
        expect(container.querySelectorAll('.subtask-list')).toHaveLength(1);
        // A row draws no list of its own: its descendants are already rows of this one.
        expect(container.querySelectorAll('.subtask-wrapper .subtask-list')).toHaveLength(0);
        expect(rowIds(container)).toEqual(['a1a', 'a1', 'a', 'b']);
    });

    it('draws one group per top-level subtask, holding exactly that branch rows', () => {
        const { container } = renderOutline(tree());
        const groups = [...container.querySelectorAll('.subtask-group')];
        expect(groups).toHaveLength(2);
        expect([...groups[0].querySelectorAll('.card')].map(el => (el as HTMLElement).dataset.taskId))
            .toEqual(['a1a', 'a1', 'a']);
        expect([...groups[1].querySelectorAll('.card')].map(el => (el as HTMLElement).dataset.taskId))
            .toEqual(['b']);
    });

    it('drops a collapsed branch rows and leaves its siblings where they were', () => {
        const t = tree();
        const { container } = renderOutline({ ...t, expanded: new Set(['root', 'a']) });
        expect(rowIds(container)).toEqual(['a1', 'a', 'b']);
    });

    it('keeps the progress bar on the card alone, counting every subtask', () => {
        const { container } = renderOutline(tree());
        const bar = container.querySelector('.card > .subtask-progress-bar');
        expect(bar).toBeTruthy();
        expect(bar?.textContent).toContain('4 subtasks');
        for (const id of rowIds(container)) {
            expect(rowWrapper(container, id).querySelector('.subtask-progress-bar')).toBeNull();
        }
    });

    it('counts in the roll-up circle the rows that one waits for', () => {
        const { container } = renderOutline(tree());
        expect(rowWrapper(container, 'a').querySelector('.rollup-count')?.textContent).toBe('2');
        expect(rowWrapper(container, 'a1').querySelector('.rollup-count')?.textContent).toBe('1');
        // A leaf waits for nothing, and holds no control.
        expect(rowWrapper(container, 'a1a').querySelector('.subtask-rollup')).toBeNull();
    });

    it('toggles its own branch from the roll-up circle', async () => {
        const { container, onToggleExpanded } = renderOutline(tree());
        const rollup = rowWrapper(container, 'a').querySelector<HTMLButtonElement>('.subtask-rollup')!;
        expect(rollup.getAttribute('aria-expanded')).toBe('true');

        await fireEvent.click(rollup);

        expect(onToggleExpanded).toHaveBeenCalledWith('a');
    });

    it('tints exactly the rows a hovered roll-up waits for', async () => {
        const { container } = renderOutline(tree());
        const rollup = rowWrapper(container, 'a').querySelector('.subtask-rollup')!;

        await fireEvent.pointerEnter(rollup);

        expect(container.querySelectorAll('.subtask-wrapper.is-waited')).toHaveLength(2);
        expect(rowWrapper(container, 'a1a').classList.contains('is-waited')).toBe(true);
        expect(rowWrapper(container, 'a1').classList.contains('is-waited')).toBe(true);
        expect(rowWrapper(container, 'a').classList.contains('is-waited')).toBe(false);
        expect(rowWrapper(container, 'b').classList.contains('is-waited')).toBe(false);
    });

    it('tints the same rows from the keyboard as from the pointer', async () => {
        const { container } = renderOutline(tree());
        const rollup = rowWrapper(container, 'a1').querySelector('.subtask-rollup')!;

        await fireEvent.focus(rollup);

        expect([...container.querySelectorAll('.subtask-wrapper.is-waited')])
            .toEqual([rowWrapper(container, 'a1a')]);

        await fireEvent.blur(rollup);
        expect(container.querySelectorAll('.subtask-wrapper.is-waited')).toHaveLength(0);
    });

    it('rings the count with the rows done, and turns it into a disc only when all are done', () => {
        const { container } = renderOutline(tree({ a1: 'done', a1a: 'done', a: 'done' }));
        const done = rowWrapper(container, 'a').querySelector('.subtask-rollup')!;
        expect(done.classList.contains('is-complete')).toBe(true);
        expect(done.querySelector('.rollup-disc')).toBeTruthy();
        expect(done.getAttribute('aria-label')).toBe('Hide the 2 rows this row waits for · 2 of 2 rows done');

        const partial = renderOutline(tree({ a1a: 'done' }));
        const rollup = rowWrapper(partial.container, 'a').querySelector('.subtask-rollup')!;
        const arc = rollup.querySelector('.rollup-arc')!;
        expect(arc).toBeTruthy();
        expect(rollup.querySelector('.rollup-disc')).toBeNull();
        expect(rollup.classList.contains('is-complete')).toBe(false);
        // The ring is drawn by the dash: a third of the circumference still missing at 1 of 2 done…
        const dashoffset = Number(/stroke-dashoffset:\s*([\d.]+)/.exec(arc.getAttribute('style') ?? '')?.[1]);
        const circumference = Number(/stroke-dasharray:\s*([\d.]+)/.exec(arc.getAttribute('style') ?? '')?.[1]);
        expect(circumference).toBeGreaterThan(0);
        expect(dashoffset).toBeCloseTo(circumference / 2, 5);
        // …and a control that says which half it waits on.
        expect(rollup.getAttribute('aria-label')).toBe('Hide the 2 rows this row waits for · 1 of 2 rows done');
    });

    it('keeps the row actions a card has', () => {
        const { container } = renderOutline(tree());
        const row = rowWrapper(container, 'a1').querySelector('.card')!;
        expect(row.querySelector('button[aria-label="Create subtask"]')).toBeTruthy();
        expect(row.querySelector('button[aria-label="Edit task"]')).toBeTruthy();
    });

    it('selects the deepest row itself, not the card that lists it', async () => {
        const { container } = renderOutline(tree());
        const row = container.querySelector<HTMLElement>('.card[data-task-id="a1a"]')!;
        expect(row).toBeTruthy();

        row.focus();
        await fireEvent.keyDown(row, { key: 'Enter', bubbles: true });

        expect((mockAppStore.selectedTask as Task | null)?.id).toBe('a1a');
    });
});

describe('Card subtask outline badge rule', () => {
    /** root runs on one/alpha; the rows vary one half, both halves, or neither. */
    function withModels() {
        const root = makeTask('root', { llama_server_url: ONE, model_name: 'alpha' });
        const same = makeTask('same', { parent_task_id: 'root', llama_server_url: ONE, model_name: 'alpha' });
        const otherModel = makeTask('othermodel', { parent_task_id: 'root', llama_server_url: ONE, model_name: 'beta' });
        const otherProvider = makeTask('otherprovider', { parent_task_id: 'root', llama_server_url: TWO, model_name: 'alpha' });
        return {
            task: root,
            childrenMap: new Map<string, Task[]>([['root', [same, otherModel, otherProvider]]]),
            expanded: new Set(['root']),
        };
    }

    function badge(container: HTMLElement, id: string): Element | null {
        return rowWrapper(container, id).querySelector('.model-badge');
    }

    it('prints no badge for a row that runs where its parent runs', () => {
        const { container } = renderOutline(withModels());
        expect(badge(container, 'same')).toBeNull();
    });

    it('prints provider and model for a row on another model, unmarked', () => {
        const { container } = renderOutline(withModels());
        const b = badge(container, 'othermodel');
        expect(b?.textContent).toContain('onemodel.local');
        expect(b?.textContent).toContain('beta');
        expect(b?.querySelector('.badge-fork')).toBeNull();
    });

    it('marks a row that runs on another provider', () => {
        const { container } = renderOutline(withModels());
        const b = badge(container, 'otherprovider');
        expect(b?.textContent).toContain('twomodel.local');
        expect(b?.querySelector('.badge-fork')).toBeTruthy();
    });

    it('hides the mark together with the badge', () => {
        mockAppStore.config = config({ hide_model_badge: true });
        const { container } = renderOutline(withModels());
        expect(badge(container, 'otherprovider')).toBeNull();
        expect(container.querySelector('.badge-fork')).toBeNull();
    });
});
