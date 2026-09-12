import { describe, it, expect, vi, beforeAll, afterEach } from 'vitest';
import { render } from '@testing-library/svelte';

// Regression: the Task View side board column must not scroll on its own while the
// task detail is open. The executor broadcasts `task_updated` for the open task
// several times a turn and App.svelte swaps in a new task object each time, so the
// reveal effect has to key on the task id — keying on the object re-centred the
// column on every frame.

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

vi.mock('$lib/stores', () => ({
    appStore: { selectedTask: null, board: null, config: { providers: [] }, editTaskData: null, showEditTask: false, selectedProject: null, projects: [] },
    uiStore: {},
    dialogStore: { showToast: vi.fn(), showConfirm: vi.fn() },
    updateTaskInBoard: vi.fn(),
    removeTaskFromBoard: vi.fn(),
}));

import BoardOverview from '$lib/components/CardDetail/BoardOverview.svelte';
import type { Task } from '$lib/types';

const SECTIONS = [
    { id: 'backlog', title: 'Backlog' },
    { id: 'in_progress', title: 'In Progress' },
    { id: 'review', title: 'Review' },
    { id: 'done', title: 'Done' },
];

function makeTask(id: string, overrides: Partial<Task> = {}): Task {
    return {
        id,
        project_id: 'p1',
        title: `Task ${id}`,
        prompt: '',
        status: 'in_progress',
        execution_state: 'running',
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
        ...overrides,
    } as unknown as Task;
}

const scrollIntoView = vi.fn();

/** Run the component's double requestAnimationFrame on the macrotask queue. */
async function drainFrames(n = 6) {
    for (let i = 0; i < n; i++) await new Promise((r) => setTimeout(r, 0));
}

function mount(task: Task, listed: Task[] = [task]) {
    return render(BoardOverview, {
        sections: SECTIONS,
        allTasks: listed,
        consolidatedSubtaskIds: new Set<string>(),
        sectionDragState: new Map(),
        collapsedSections: new Set<string>(),
        currentTask: task,
        childrenMap: new Map<string, Task[]>(),
        expandedTasks: new Set<string>(),
        onToggleSection: () => {},
        onSectionDragOver: () => {},
        onSectionDragLeave: () => {},
        onSectionDrop: () => {},
        onClearDone: async () => {},
        onToggleExpanded: () => {},
        onCreateChildTask: () => {},
        onShowCreateTask: () => {},
        getColumnColor: () => 'red',
    });
}

beforeAll(() => {
    vi.stubGlobal('matchMedia', (q: string) => ({
        matches: false, media: q, onchange: null,
        addEventListener: () => {}, removeEventListener: () => {},
        addListener: () => {}, removeListener: () => {}, dispatchEvent: () => false,
    }));
    // jsdom's rAF runs on a ~16ms clock; make the double-rAF deterministic.
    vi.stubGlobal('requestAnimationFrame', (cb: FrameRequestCallback) => setTimeout(() => cb(Date.now()), 0) as unknown as number);
    Element.prototype.scrollIntoView = scrollIntoView;
});

afterEach(() => scrollIntoView.mockClear());

describe('BoardOverview — revealing the current task', () => {
    it('centers the current task\'s card on mount', async () => {
        mount(makeTask('running-task'));
        await drainFrames();
        expect(scrollIntoView).toHaveBeenCalledTimes(1);
        expect(scrollIntoView).toHaveBeenCalledWith({ behavior: 'auto', block: 'center' });
    });

    it('does NOT re-center when the task is replaced by an equal-valued new object (task_updated)', async () => {
        const task = makeTask('running-task');
        const { rerender } = mount(task);
        await drainFrames();
        expect(scrollIntoView).toHaveBeenCalledTimes(1);

        // A turn sends several task_updated frames; each carries a new object for
        // the same task (execution_state, summarizing, status, ...).
        for (const patch of [{}, { summarizing: true }, { summarizing: false }, { updated_at: new Date().toISOString() }]) {
            rerender({ currentTask: { ...task, ...patch } as Task });
            await drainFrames();
        }

        expect(scrollIntoView).toHaveBeenCalledTimes(1);
    });

    it('does NOT re-center when the same object instance is re-passed', async () => {
        const task = makeTask('running-task');
        const { rerender } = mount(task);
        await drainFrames();
        rerender({ currentTask: task });
        await drainFrames();
        expect(scrollIntoView).toHaveBeenCalledTimes(1);
    });

    it('re-centers when the task actually changes', async () => {
        const first = makeTask('task-a');
        const { rerender } = mount(first);
        await drainFrames();
        expect(scrollIntoView).toHaveBeenCalledTimes(1);

        const second = makeTask('task-b');
        rerender({ currentTask: second, allTasks: [first, second] });
        await drainFrames();
        expect(scrollIntoView).toHaveBeenCalledTimes(2);
    });

    it('leaves a card that is already fully visible alone', async () => {
        // Give an ancestor a real scroll range and place the card inside it.
        Object.defineProperty(HTMLElement.prototype, 'scrollHeight', { configurable: true, get: () => 1000 });
        Object.defineProperty(HTMLElement.prototype, 'clientHeight', { configurable: true, get: () => 300 });
        const originalRect = Element.prototype.getBoundingClientRect;
        Element.prototype.getBoundingClientRect = function (this: Element) {
            // The scroller spans 0..300; the card sits well inside it.
            return this.classList.contains('card')
                ? ({ top: 40, bottom: 100 } as DOMRect)
                : ({ top: 0, bottom: 300 } as DOMRect);
        };
        document.body.style.overflowY = 'auto';

        try {
            mount(makeTask('visible-task'));
            await drainFrames();
            expect(scrollIntoView).not.toHaveBeenCalled();
        } finally {
            document.body.style.overflowY = '';
            Element.prototype.getBoundingClientRect = originalRect;
            delete (HTMLElement.prototype as unknown as { scrollHeight?: number }).scrollHeight;
            delete (HTMLElement.prototype as unknown as { clientHeight?: number }).clientHeight;
        }
    });

    it('does not reach outside its own list for the card', async () => {
        // A floating task view renders the same task in a second instance; never
        // grab a card outside this component's own list. The task isn't listed at
        // all here, so only the foreign card exists.
        const outside = document.createElement('div');
        outside.className = 'card';
        outside.setAttribute('data-task-id', 'ghost-task');
        document.body.appendChild(outside);
        try {
            mount(makeTask('ghost-task'), []);
            await drainFrames();
            expect(scrollIntoView).not.toHaveBeenCalled();
        } finally {
            outside.remove();
        }
    });
});
