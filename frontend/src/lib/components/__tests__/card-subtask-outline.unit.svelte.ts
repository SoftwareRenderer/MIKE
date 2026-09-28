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
import { diffStatsStore } from '$lib/stores/diff-stats.svelte';
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
        // Fixed, never `new Date()`: `sortByLastMoved` has no tiebreaker, so a fixture that let the clock
        // advance between two tasks reorders the siblings, and every expected row list would then depend on
        // when the file ran. A case that wants an order passes its own `moved_at`.
        created_at: '2026-01-01T00:00:00.000Z',
        updated_at: '2026-01-01T00:00:00.000Z',
        moved_at: '2026-01-01T00:00:00.000Z',
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

    it('keeps the row actions a card has', () => {
        const { container } = renderOutline(tree());
        const row = rowWrapper(container, 'a1').querySelector('.card')!;
        expect(row.querySelector('button[aria-label="Create subtask"]')).toBeTruthy();
        expect(row.querySelector('button[aria-label="Edit task"]')).toBeTruthy();
    });

    it('holds the status word on the title line and what the row did under it', () => {
        // The reserve that keeps a row clear of its action buttons is paid by the one line the status word
        // shares with the title. The second line is the row's own: the file count and the model it ran on.
        const t = tree();
        t.tasks.a.model_name = 'beta';
        t.tasks.a.execution_state = 'paused';
        diffStatsStore.set('a', { hash: 'deadbeef', files: 2, additions: 4, deletions: 1, staged_paths: ['x.ts', 'y.ts'] });
        try {
            const { container } = renderOutline(t);
            const listing = container.querySelector('.card')!;
            const row = rowWrapper(container, 'a').querySelector(':scope > .card')!;
            const title = row.querySelector('.card-title')!;
            const second = row.querySelector(':scope > .diff-stats-row')!;
            expect(title.querySelector('.badges-row.is-inline')?.textContent).toContain('Paused');
            expect(title.querySelector('.model-badge')).toBeNull();
            expect(second.querySelector('.diff-files')?.textContent).toBe('2 files');
            expect(second.querySelector('.model-badge')?.textContent).toContain('beta');
            // The card that lists the rows keeps no second line of its own under their indent.
            expect(listing.querySelector(':scope > .diff-stats-row')).toBeNull();
        } finally {
            diffStatsStore.remove('a');
        }
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
