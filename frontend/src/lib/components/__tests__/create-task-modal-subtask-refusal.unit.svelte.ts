import { describe, it, expect, vi, beforeAll, beforeEach, afterEach } from 'vitest';
import { render, cleanup, waitFor, fireEvent } from '@testing-library/svelte';

vi.mock('svelte/transition', () => ({ slide: vi.fn(), fade: vi.fn() }));

// The vitest setup no-ops onMount; this flow is its onMount, so run the callbacks.
vi.mock('svelte', async (importOriginal) => {
    const svelte = await importOriginal<typeof import('svelte')>();
    return {
        ...svelte,
        onMount: (cb: () => void | Promise<void>) => { void Promise.resolve().then(cb); },
    };
});

// isTransientError stays real: telling a refusal from a network failure is what is tested.
const api = vi.hoisted(() => ({
    createTask: vi.fn(),
    getTask: vi.fn(),
    enqueueAction: vi.fn(),
    setOfflineStatus: vi.fn(),
    startTask: vi.fn(),
}));

vi.mock('$lib/api', async (importOriginal) => ({
    ...(await importOriginal<typeof import('$lib/api')>()),
    createTask: api.createTask,
    getTask: api.getTask,
    enqueueAction: api.enqueueAction,
    setOfflineStatus: api.setOfflineStatus,
    startTask: api.startTask,
    getOfflineStatus: () => false,
}));

import CreateTaskModal from '$lib/components/CreateTaskModal.svelte';
import { appStore, dialogStore } from '$lib/stores';
import type { Task } from '$lib/types';

beforeAll(() => {
    vi.stubGlobal('matchMedia', (query: string) => ({
        matches: false, media: query, onchange: null,
        addEventListener: () => {}, removeEventListener: () => {},
        addListener: () => {}, removeListener: () => {},
        dispatchEvent: () => false,
    }));
});

/** The parent is in progress, so its children are created Pending beneath it. */
const PARENT_ID = 'frost-amber-quartz';

function seededBoard() {
    appStore.board = { project_id: 'p1', tasks: [] } as never;
}

async function submitSubtask() {
    const { container } = render(CreateTaskModal, {
        props: { projectId: 'p1', parentId: PARENT_ID, onClose: () => {} },
    });
    // Waiting on the parent's title proves the fetch that also brings its status landed.
    await waitFor(() => expect(container.querySelector('.parent-label strong')?.textContent).toBe('Started parent'));
    const textarea = container.querySelector('textarea') as HTMLTextAreaElement;
    await fireEvent.input(textarea, { target: { value: 'split the work further' } });
    await fireEvent.click(container.querySelector('.btn-primary') as HTMLButtonElement);
    return container;
}

describe('CreateTaskModal — a subtask of a started task', () => {
    let toast: ReturnType<typeof vi.spyOn>;

    beforeAll(() => {
        api.getTask.mockResolvedValue({
            id: PARENT_ID, title: 'Started parent', status: 'in_progress',
            auto_commit: false, subtasks_auto_merge: false,
        });
        api.enqueueAction.mockResolvedValue({ id: 'q1' } as never);
    });

    beforeEach(() => {
        seededBoard();
        toast = vi.spyOn(dialogStore, 'showToast').mockImplementation(() => undefined);
    });

    afterEach(() => {
        cleanup();
        vi.clearAllMocks();
        appStore.board = null;
    });

    it('is placed Pending beneath its parent, not in the backlog', async () => {
        api.createTask.mockReturnValue(new Promise(() => {})); // still in flight when we look
        await submitSubtask();

        await waitFor(() => expect(appStore.board?.tasks).toHaveLength(1));
        const card = appStore.board!.tasks[0] as Task;
        expect(card.status).toBe('in_progress');
        expect(card.execution_state).toBe('idle'); // the "Pending" badge
    });

    it('shows a server refusal and drops the card instead of queueing it', async () => {
        api.createTask.mockRejectedValue(new Error('parent task is archived'));
        await submitSubtask();

        await waitFor(() => expect(toast).toHaveBeenCalled());
        expect(toast.mock.calls[0][0]).toContain('parent task is archived');
        expect(appStore.board?.tasks).toHaveLength(0);
        // A refusal is not a lost connection: nothing queued, nothing marked offline.
        expect(api.setOfflineStatus).not.toHaveBeenCalled();
        expect(api.enqueueAction).not.toHaveBeenCalled();
    });

    it('still queues the create when the network is what failed', async () => {
        api.createTask.mockRejectedValue(new TypeError('Failed to fetch'));
        await submitSubtask();

        await waitFor(() => expect(api.enqueueAction).toHaveBeenCalled());
        expect(api.setOfflineStatus).toHaveBeenCalledWith(true);
        expect(appStore.board?.tasks).toHaveLength(1); // the card stays, promising a retry
        // The queued body carries the column, so the replay lands it under its parent.
        const queued = api.enqueueAction.mock.calls[0][0] as { type: string; params: { taskData: Task } };
        expect(queued.type).toBe('createTask');
        expect(queued.params.taskData.status).toBe('in_progress');
    });
});
