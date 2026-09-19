import { describe, it, expect } from 'vitest';
import type { Task } from '$lib/types';
import {
    reviewBaseline,
    tasksInReview,
    reviewArrivals,
    visibleFraction,
    columnInViewport,
    isLookingAtReview,
    notableArrivals,
    planReviewNotice,
    dismissedTags,
    type AttentionState,
    type ShownNotice,
} from '$lib/utils/review-notify';

function task(id: string, status: string, movedAt = '2026-01-01T00:00:00Z'): Task {
    return {
        id,
        project_id: 'proj',
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

const review = (id: string, movedAt?: string) => task(id, 'review', movedAt);

describe('tasksInReview', () => {
    it('takes the Review column out of a board', () => {
        const board = [task('a', 'backlog'), review('b'), task('c', 'done'), review('d')];
        expect(tasksInReview(board).map(t => t.id)).toEqual(['b', 'd']);
    });
});

describe('reviewArrivals', () => {
    it('names the cards that were not in the column before', () => {
        const baseline = reviewBaseline(1, ['old']);
        expect(reviewArrivals(baseline, [review('old'), review('new-a'), review('new-b')]).map(t => t.id))
            .toEqual(['new-a', 'new-b']);
    });

    it('diffs by identity, so a card that replaced another is still an arrival', () => {
        // The caller asks only after the count went up; the diff itself ignores the count.
        const baseline = reviewBaseline(1, ['gone']);
        expect(reviewArrivals(baseline, [review('arrived')]).map(t => t.id)).toEqual(['arrived']);
    });

    it('treats a column that was empty as all-arrivals when its ids were never read', () => {
        const baseline = reviewBaseline(0, null);
        expect(reviewArrivals(baseline, [review('a'), review('b')]).map(t => t.id)).toEqual(['a', 'b']);
    });

    it('falls back to the newest move when the count rose over an unknown set', () => {
        const baseline = reviewBaseline(2, null);
        const arrived = reviewArrivals(baseline, [
            review('stale', '2026-01-01T00:00:00Z'),
            review('just-landed', '2026-09-16T10:00:00Z'),
            review('older', '2026-03-01T00:00:00Z'),
        ]);
        expect(arrived.map(t => t.id)).toEqual(['just-landed']);
    });

    it('names nothing while the column holds nothing', () => {
        expect(reviewArrivals(reviewBaseline(1, null), [])).toEqual([]);
        expect(reviewArrivals(reviewBaseline(0, null), [])).toEqual([]);
    });

    it('prefers the id diff over the moved_at guess', () => {
        // A card that entered Review long ago and never moved again is still new to this client.
        const baseline = reviewBaseline(1, ['resident']);
        expect(reviewArrivals(baseline, [
            review('resident', '2026-01-01T00:00:00Z'),
            review('arrived', '2026-01-02T00:00:00Z'),
        ]).map(t => t.id)).toEqual(['arrived']);
    });
});

describe('columnInViewport', () => {
    const viewport = { start: 0, end: 1000 };

    it('counts a fully visible column as visible', () => {
        expect(visibleFraction(viewport, { start: 600, end: 800 })).toBe(1);
        expect(columnInViewport(viewport, { start: 600, end: 800 })).toBe(true);
    });

    it('counts a column scrolled off the board as hidden', () => {
        expect(visibleFraction(viewport, { start: 1200, end: 1500 })).toBe(0);
        expect(columnInViewport(viewport, { start: 1200, end: 1500 })).toBe(false);
    });

    it('splits at a majority of the column, whichever edge is cut', () => {
        // 200 of 500 visible: passing a glance, not looking at it.
        expect(visibleFraction(viewport, { start: 800, end: 1300 })).toBeCloseTo(0.4);
        expect(columnInViewport(viewport, { start: 800, end: 1300 })).toBe(false);
        // 300 of 500 visible.
        expect(visibleFraction(viewport, { start: -200, end: 300 })).toBeCloseTo(0.6);
        expect(columnInViewport(viewport, { start: -200, end: 300 })).toBe(true);
    });

    it('treats a column with no width as hidden rather than infinitely visible', () => {
        expect(visibleFraction(viewport, { start: 10, end: 10 })).toBe(0);
        expect(columnInViewport(viewport, { start: 10, end: 10 })).toBe(false);
    });

    it('honours a threshold the caller asks for', () => {
        expect(columnInViewport(viewport, { start: 800, end: 1300 }, 0.3)).toBe(true);
    });
});

describe('isLookingAtReview', () => {
    const onReview: AttentionState = { documentVisible: true, documentFocused: true, reviewProject: 'p1' };

    it('is only true with the board, the Review column and the person all present', () => {
        expect(isLookingAtReview(onReview)).toBe(true);
        expect(isLookingAtReview({ ...onReview, documentVisible: false })).toBe(false);
        expect(isLookingAtReview({ ...onReview, documentFocused: false })).toBe(false);
        expect(isLookingAtReview({ ...onReview, reviewProject: null })).toBe(false);
    });
});

describe('notableArrivals', () => {
    const arrivals = [review('opened'), review('moved-by-me'), review('someone-else')];

    it('drops the task the user already has open', () => {
        expect(notableArrivals(arrivals, 'opened', []).map(t => t.id)).toEqual(['moved-by-me', 'someone-else']);
    });

    it('drops the moves this tab made itself', () => {
        expect(notableArrivals(arrivals, null, ['moved-by-me']).map(t => t.id)).toEqual(['opened', 'someone-else']);
    });

    it('keeps everything when nothing is open and this tab moved nothing', () => {
        expect(notableArrivals(arrivals, null, []).map(t => t.id))
            .toEqual(['opened', 'moved-by-me', 'someone-else']);
    });
});

describe('planReviewNotice', () => {
    const group = (projectId: string, ...ids: string[]) => ({ projectId, tasks: ids.map(id => review(id)) });
    /** Nobody is looking at anything until a case says so. */
    const blind: AttentionState = { documentVisible: false, documentFocused: false, reviewProject: null };
    const plan = (
        groups: { projectId: string; tasks: Task[] }[],
        reviewCounts: Record<string, number>,
        reviewProject: string | null = null,
        attention: Partial<AttentionState> = {},
    ) => planReviewNotice(groups, reviewCounts, {
        documentVisible: true,
        documentFocused: true,
        reviewProject,
        ...attention,
    });

    it('opens the task when one card arrived', () => {
        expect(plan([group('p1', 'solo')], { p1: 1 })).toEqual({ projectId: 'p1', count: 1, task: review('solo') });
    });

    it('opens the board when several cards arrived, and counts them all', () => {
        const notice = plan([group('p1', 'a', 'b', 'c')], { p1: 3 });
        expect(notice?.task).toBeNull();
        expect(notice).toMatchObject({ projectId: 'p1', count: 3 });
    });

    it('is one notification across projects, pointing at the fullest Review column', () => {
        // Ingest received more of the arrivals, but Payments has the bigger pile to wade through.
        const notice = plan(
            [group('ingest', 'i1', 'i2'), group('payments', 'p1')],
            { ingest: 2, payments: 9 },
        );
        expect(notice).toMatchObject({ projectId: 'payments', count: 3, task: null });
    });

    it('counts what is waiting, not what arrived, when the frame knows the column', () => {
        const notice = plan([group('small', 'a'), group('large', 'b')], { small: 1, large: 40 });
        expect(notice?.projectId).toBe('large');
    });

    it('ranks by the arrivals it has when no count was ever read for a project', () => {
        const notice = plan([group('unread', 'a'), group('other', 'b', 'c')], {});
        expect(notice?.projectId).toBe('other');
    });

    it('settles a tie on waiting by who had more arrivals, then by project id', () => {
        expect(plan([group('aaa', 'a'), group('bbb', 'b', 'c')], { aaa: 5, bbb: 5 })?.projectId).toBe('bbb');
        expect(plan([group('zzz', 'a'), group('aaa', 'b')], { zzz: 5, aaa: 5 })?.projectId).toBe('aaa');
    });

    it('drops the arrivals of the project whose Review column is in view', () => {
        const notice = plan(
            [group('watched', 'w1', 'w2'), group('elsewhere', 'e1')],
            { watched: 9, elsewhere: 1 },
            'watched',
        );
        expect(notice).toEqual({ projectId: 'elsewhere', count: 1, task: review('e1') });
    });

    it('says nothing when the only arrivals are in front of the user', () => {
        expect(plan([group('watched', 'w1')], { watched: 2 }, 'watched')).toBeNull();
    });

    it('tells a background tab about the arrivals of the board it would otherwise be staring at', () => {
        const unfocused = plan([group('own', 'a')], { own: 2 }, 'own', { documentFocused: false });
        expect(unfocused).toEqual({ projectId: 'own', count: 1, task: review('a') });
        expect(plan([group('own', 'a')], { own: 2 }, 'own', { documentVisible: false })).not.toBeNull();
        // A tab that is neither visible nor focused is looking at nothing, wherever its board is.
        expect(planReviewNotice([group('own', 'a')], { own: 2 }, blind)).not.toBeNull();
    });

    it('says nothing when every group is empty', () => {
        expect(plan([group('p1'), group('p2')], { p1: 4, p2: 4 })).toBeNull();
        expect(plan([], {})).toBeNull();
    });
});

describe('dismissedTags', () => {
    /** One batch at a time: its tag replaces itself, so the ledger holds a single one, pointing at p1. */
    const shown: ShownNotice[] = [
        { tag: 'mike-task-t1', projectId: 'p1', taskId: 't1' },
        { tag: 'mike-task-t2', projectId: 'p1', taskId: 't2' },
        { tag: 'mike-review-batch', projectId: 'p1', taskId: null },
        { tag: 'mike-task-t9', projectId: 'p2', taskId: 't9' },
    ];

    it('retires nothing while no task is open and no Review column is in view', () => {
        expect(dismissedTags(shown, null, null)).toEqual([]);
    });

    it('retires only the notice that names the open task', () => {
        expect(dismissedTags(shown, 't1', null)).toEqual(['mike-task-t1']);
    });

    it('leaves a batch alone when one of the tasks it speaks for is opened', () => {
        expect(dismissedTags(shown, 't2', null)).not.toContain('mike-review-batch');
    });

    it('retires every notice of the project whose Review column is in view, batch included', () => {
        expect(dismissedTags(shown, null, 'p1')).toEqual(['mike-task-t1', 'mike-task-t2', 'mike-review-batch']);
    });

    it('leaves another project alone however closely this one is being read', () => {
        expect(dismissedTags(shown, 't1', 'p1')).not.toContain('mike-task-t9');
        // The batch points at p1's board, so reading p2 retires only what p2 owns.
        expect(dismissedTags(shown, null, 'p2')).toEqual(['mike-task-t9']);
    });

    it('answers both rules at once', () => {
        expect(dismissedTags(shown, 't9', 'p1'))
            .toEqual(['mike-task-t1', 'mike-task-t2', 'mike-review-batch', 'mike-task-t9']);
    });
});
