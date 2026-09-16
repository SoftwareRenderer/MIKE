import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import type { Config, Project, Task } from '$lib/types';
import { appStore, uiStore } from '$lib/stores';

const { getBoard, show } = vi.hoisted(() => ({ getBoard: vi.fn(), show: vi.fn() }));
vi.mock('$lib/api/projects', () => ({ getBoard: (projectId: string) => getBoard(projectId) }));
vi.mock('$lib/utils/notifications', () => ({ showBrowserNotification: (spec: unknown) => show(spec) }));

import { reviewNotifier } from '$lib/services/review-notifications.svelte';

function task(id: string, status: string, movedAt = '2026-01-01T00:00:00Z'): Task {
    return {
        id,
        project_id: 'p',
        title: id,
        prompt: 'p',
        status,
        execution_state: 'idle',
        has_merge_conflict: false,
        max_consecutive_failed_turns: 0,
        auto_commit: false,
        auto_merge: false,
        subtasks_auto_merge: false,
        operating_mode: 'act',
        title_generating: false,
        summarizing: false,
        created_at: '2026-01-01T00:00:00Z',
        updated_at: '2026-01-01T00:00:00Z',
        moved_at: movedAt,
    };
}

const reviewOf = (id: string, movedAt?: string) => task(id, 'review', movedAt);

/** One project in a `project_counts` frame — the shape the server sends for every project. */
function counts(projectId: string, review: number) {
    return { [projectId]: { backlog: 0, in_progress: 0, review } };
}

function setPreference(on: boolean) {
    appStore.config = { notify_on_review: on } as unknown as Config;
}

let projectSeq = 0;
/** A project per case: the notifier's ledger is per project, so cases cannot leak into each other. */
function freshProject(name = 'Payments') {
    const id = `proj-${++projectSeq}`;
    appStore.projects = [{ id, name } as Project];
    return id;
}

function setVisibility({ visible = true, focused = true }: { visible?: boolean; focused?: boolean } = {}) {
    Object.defineProperty(document, 'visibilityState', { configurable: true, get: () => (visible ? 'visible' : 'hidden') });
    vi.spyOn(document, 'hasFocus').mockReturnValue(focused);
}

beforeEach(() => {
    getBoard.mockReset();
    show.mockReset();
    setPreference(true);
    setVisibility();
    uiStore.reviewColumnInViewOf = null;
    uiStore.floatingTask = null;
    appStore.selectedTask = null;
    reviewNotifier.clearBoard();
});

afterEach(() => {
    vi.restoreAllMocks();
    delete (document as unknown as { visibilityState?: string }).visibilityState;
});

describe('reviewNotifier while the preference is off', () => {
    it('reads nothing and notifies nothing', () => {
        setPreference(false);
        const projectId = freshProject();
        reviewNotifier.observeCounts(counts(projectId, 0));
        reviewNotifier.observeCounts(counts(projectId, 3));
        expect(getBoard).not.toHaveBeenCalled();
        expect(show).not.toHaveBeenCalled();
    });

    it('does not spend the change it missed when the preference is switched on later', async () => {
        setPreference(false);
        const projectId = freshProject();
        reviewNotifier.observeCounts(counts(projectId, 0));
        reviewNotifier.observeCounts(counts(projectId, 3));
        getBoard.mockResolvedValue({ tasks: [
            reviewOf('one'), reviewOf('two'), reviewOf('three'),
            reviewOf('four', '2026-09-16T09:00:00Z'),
        ] });
        setPreference(true);
        reviewNotifier.observeCounts(counts(projectId, 3));
        expect(show).not.toHaveBeenCalled();
        reviewNotifier.observeCounts(counts(projectId, 4));
        await vi.waitFor(() => expect(show).toHaveBeenCalledTimes(1));
        expect(show.mock.calls[0][0].title).toBe('four');
    });
});

describe('reviewNotifier arrivals in another project', () => {
    it('counts the first frame without telling anyone about it', () => {
        const projectId = freshProject();
        reviewNotifier.observeCounts(counts(projectId, 2));
        expect(getBoard).not.toHaveBeenCalled();
        expect(show).not.toHaveBeenCalled();
    });

    it('names the task that arrived and the project it arrived in', async () => {
        const projectId = freshProject('Ingest');
        getBoard.mockResolvedValue({ tasks: [reviewOf('resident', '2026-01-01T00:00:00Z'), reviewOf('just-landed', '2026-09-16T09:00:00Z')] });
        reviewNotifier.observeCounts(counts(projectId, 1));
        reviewNotifier.observeCounts(counts(projectId, 2));
        await vi.waitFor(() => expect(show).toHaveBeenCalledTimes(1));
        const spec = show.mock.calls[0][0];
        expect(spec.title).toBe('just-landed');
        expect(spec.body).toContain('Ingest');
        expect(spec.tag).toBe('mike-task-just-landed');
        expect(spec.url).toContain(`project=${projectId}`);
        expect(spec.url).toContain('task=just-landed');
    });

    it('says how many arrived when several landed at once', async () => {
        const projectId = freshProject();
        getBoard.mockResolvedValue({ tasks: [reviewOf('a', '2026-09-16T09:00:00Z'), reviewOf('b', '2026-09-16T09:01:00Z')] });
        reviewNotifier.observeCounts(counts(projectId, 0));
        reviewNotifier.observeCounts(counts(projectId, 2));
        await vi.waitFor(() => expect(show).toHaveBeenCalledTimes(1));
        expect(show.mock.calls[0][0].title).toBe('2 tasks waiting in Review');
    });

    it('announces one arrival once, whichever signal reports it first', async () => {
        const projectId = freshProject();
        getBoard.mockResolvedValue({ tasks: [reviewOf('resident'), reviewOf('new-comer', '2026-09-16T09:00:00Z')] });
        reviewNotifier.observeCounts(counts(projectId, 1));
        reviewNotifier.observeCounts(counts(projectId, 2));
        await vi.waitFor(() => expect(show).toHaveBeenCalledTimes(1));
        // The counts frame for the same state arrives again (another client, another write).
        reviewNotifier.observeCounts(counts(projectId, 2));
        reviewNotifier.observeCounts(counts(projectId, 2));
        expect(show).toHaveBeenCalledTimes(1);
    });

    it('asks again once the announced task has left Review', async () => {
        const projectId = freshProject();
        getBoard.mockResolvedValue({ tasks: [reviewOf('round-one')] });
        reviewNotifier.observeCounts(counts(projectId, 0));
        reviewNotifier.observeCounts(counts(projectId, 1));
        await vi.waitFor(() => expect(show).toHaveBeenCalledTimes(1));

        getBoard.mockResolvedValue({ tasks: [] });
        reviewNotifier.observeCounts(counts(projectId, 0));
        getBoard.mockResolvedValue({ tasks: [reviewOf('round-one', '2026-09-16T10:00:00Z')] });
        reviewNotifier.observeCounts(counts(projectId, 1));
        await vi.waitFor(() => expect(show).toHaveBeenCalledTimes(2));
    });

    it('says nothing when the board says nothing changed', async () => {
        const projectId = freshProject();
        getBoard.mockResolvedValue({ tasks: [reviewOf('resident')] });
        reviewNotifier.observeCounts(counts(projectId, 1));
        reviewNotifier.observeCounts(counts(projectId, 2));
        await vi.waitFor(() => expect(getBoard).toHaveBeenCalled());
        expect(show).not.toHaveBeenCalled();
    });

    it('survives a board read that fails, and asks again on the next rise', async () => {
        const projectId = freshProject();
        getBoard.mockRejectedValue(new Error('offline'));
        reviewNotifier.observeCounts(counts(projectId, 0));
        reviewNotifier.observeCounts(counts(projectId, 1));
        await vi.waitFor(() => expect(getBoard).toHaveBeenCalledTimes(1));
        expect(show).not.toHaveBeenCalled();

        getBoard.mockResolvedValue({ tasks: [reviewOf('resident'), reviewOf('kept-going', '2026-09-16T09:00:00Z')] });
        reviewNotifier.observeCounts(counts(projectId, 2));
        await vi.waitFor(() => expect(show).toHaveBeenCalledTimes(1));
        expect(show.mock.calls[0][0].title).toBe('kept-going');
    });
});

describe('reviewNotifier with the board on screen', () => {
    it('reads its Review column off the board instead of fetching it', () => {
        const projectId = freshProject();
        reviewNotifier.observeBoard(projectId, [task('a', 'backlog'), reviewOf('first')]);
        reviewNotifier.observeBoard(projectId, [reviewOf('first'), reviewOf('second', '2026-09-16T09:00:00Z')]);
        expect(getBoard).not.toHaveBeenCalled();
        expect(show).toHaveBeenCalledTimes(1);
        expect(show.mock.calls[0][0].title).toBe('second');
    });

    it('treats the first board for a project as what is, not as what arrived', () => {
        const projectId = freshProject();
        reviewNotifier.observeBoard(projectId, [reviewOf('already-there'), reviewOf('also-there')]);
        expect(show).not.toHaveBeenCalled();
    });

    it('treats returning to a project as a fresh look, not as arrivals', () => {
        const projectId = freshProject();
        reviewNotifier.observeBoard(projectId, [reviewOf('one')]);
        // Switching projects clears the board, and the next project's board is a first look.
        reviewNotifier.clearBoard();
        reviewNotifier.observeBoard('other-project', [reviewOf('x')]);
        reviewNotifier.clearBoard();
        reviewNotifier.observeBoard(projectId, [reviewOf('one'), reviewOf('two', '2026-09-16T09:00:00Z')]);
        expect(show).not.toHaveBeenCalled();
    });

    it('drops a project whose board was replaced by another project’s', async () => {
        const projectId = freshProject();
        reviewNotifier.observeBoard(projectId, [reviewOf('one')]);
        reviewNotifier.observeBoard('other-project', []);
        // The counts rise for the project that is no longer on screen, so it is fetched.
        getBoard.mockResolvedValue({ tasks: [reviewOf('one'), reviewOf('two', '2026-09-16T09:00:00Z')] });
        reviewNotifier.observeCounts(counts(projectId, 1));
        reviewNotifier.observeCounts(counts(projectId, 2));
        await vi.waitFor(() => expect(show).toHaveBeenCalledTimes(1));
    });
});

describe('reviewNotifier while the user is looking', () => {
    async function rise(projectId: string, before: number, after: number, arrived: string) {
        getBoard.mockResolvedValue({ tasks: [reviewOf('resident'), reviewOf(arrived, '2026-09-16T09:00:00Z')] });
        reviewNotifier.observeCounts(counts(projectId, before));
        reviewNotifier.observeCounts(counts(projectId, after));
        await vi.waitFor(() => expect(getBoard).toHaveBeenCalled());
    }

    it('stays quiet while the Review column of the project in view is the one that grew', async () => {
        const projectId = freshProject();
        uiStore.reviewColumnInViewOf = projectId;
        await rise(projectId, 1, 2, 'arrived');
        expect(show).not.toHaveBeenCalled();
    });

    it('reports an arrival elsewhere while the Review column of another project is in view', async () => {
        const lookedAt = freshProject('Being watched');
        const elsewhere = freshProject('Elsewhere');
        uiStore.reviewColumnInViewOf = lookedAt;
        await rise(elsewhere, 1, 2, 'arrived');
        expect(show).toHaveBeenCalledTimes(1);
    });

    it('notifies when that same board is only in a background tab', async () => {
        const projectId = freshProject();
        uiStore.reviewColumnInViewOf = projectId;
        setVisibility({ focused: false });
        await rise(projectId, 1, 2, 'arrived');
        expect(show).toHaveBeenCalledTimes(1);
    });

    it('notifies when the tab is hidden', async () => {
        const projectId = freshProject();
        uiStore.reviewColumnInViewOf = projectId;
        setVisibility({ visible: false });
        await rise(projectId, 1, 2, 'arrived');
        expect(show).toHaveBeenCalledTimes(1);
    });

    it('stays quiet about the task the user already has open', async () => {
        const projectId = freshProject();
        appStore.selectedTask = task('arrived', 'review');
        await rise(projectId, 1, 2, 'arrived');
        expect(show).not.toHaveBeenCalled();
    });

    it('stays quiet about the task open in the floating chat', async () => {
        const projectId = freshProject();
        uiStore.floatingTask = { projectId, taskId: 'arrived', task: task('arrived', 'review') };
        await rise(projectId, 1, 2, 'arrived');
        expect(show).not.toHaveBeenCalled();
    });

    it('notifies about another task while one is open', async () => {
        const projectId = freshProject();
        appStore.selectedTask = task('something-else', 'in_progress');
        await rise(projectId, 1, 2, 'arrived');
        expect(show).toHaveBeenCalledTimes(1);
    });
});

describe('reviewNotifier and moves this tab made itself', () => {
    it('does not report a card the user dragged into Review', async () => {
        const projectId = freshProject();
        getBoard.mockResolvedValue({ tasks: [reviewOf('resident'), reviewOf('my-card', '2026-09-16T09:00:00Z')] });
        reviewNotifier.noteSelfMove('my-card');
        reviewNotifier.observeCounts(counts(projectId, 1));
        reviewNotifier.observeCounts(counts(projectId, 2));
        await vi.waitFor(() => expect(getBoard).toHaveBeenCalled());
        expect(show).not.toHaveBeenCalled();
    });

    it('reports the same task when someone else moves it in much later', async () => {
        // Only the clock is faked: the board read still resolves on real microtasks.
        vi.useFakeTimers({ toFake: ['Date'] });
        try {
            const projectId = freshProject();
            getBoard.mockResolvedValue({ tasks: [reviewOf('resident'), reviewOf('my-card', '2026-09-16T09:00:00Z')] });
            reviewNotifier.noteSelfMove('my-card');
            vi.setSystemTime(Date.now() + 5 * 60_000);
            reviewNotifier.observeCounts(counts(projectId, 1));
            reviewNotifier.observeCounts(counts(projectId, 2));
            await vi.waitFor(() => expect(show).toHaveBeenCalledTimes(1));
        } finally {
            vi.useRealTimers();
        }
    });
});
