import { describe, it, expect } from 'vitest';
import type { Task } from '$lib/types';
import {
    reviewBaseline,
    tasksInReview,
    reviewArrivals,
    visibleFraction,
    columnInViewport,
    isLookingAtReview,
    shouldNotifyReview,
    notableArrivals,
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
    const onReview = { documentVisible: true, documentFocused: true, reviewColumnInView: true };

    it('is only true with the board, the Review column and the person all present', () => {
        expect(isLookingAtReview(onReview)).toBe(true);
        expect(isLookingAtReview({ ...onReview, documentVisible: false })).toBe(false);
        expect(isLookingAtReview({ ...onReview, documentFocused: false })).toBe(false);
        expect(isLookingAtReview({ ...onReview, reviewColumnInView: false })).toBe(false);
    });
});

describe('shouldNotifyReview', () => {
    it('notifies unless the preference is off or the Review column is in front of them', () => {
        const looking = { documentVisible: true, documentFocused: true, reviewColumnInView: true };
        const elsewhere = { documentVisible: true, documentFocused: true, reviewColumnInView: false };
        expect(shouldNotifyReview(true, elsewhere)).toBe(true);
        expect(shouldNotifyReview(true, looking)).toBe(false);
        expect(shouldNotifyReview(false, elsewhere)).toBe(false);
        expect(shouldNotifyReview(false, looking)).toBe(false);
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
