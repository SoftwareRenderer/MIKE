import { describe, it, expect } from 'vitest';
import { sortByLastMoved } from '$lib/utils/sort-tasks';

function makeTask(id: string, movedAt: string): ReturnType<typeof sortByLastMoved>[number] {
    return {
        id,
        project_id: 'proj',
        title: 'test',
        prompt: 'test',
        status: 'backlog',
        execution_state: 'idle',
        has_merge_conflict: false,
        max_consecutive_failed_turns: 0,
        auto_commit: false,
        auto_merge: false,
        subtasks_auto_merge: false,
        operating_mode: 'act',
        title_generating: false,
        summarizing: false,
        created_at: '2025-01-01T00:00:00Z',
        updated_at: '2025-06-01T00:00:00Z',
        moved_at: movedAt,
    };
}

describe('sortByLastMoved', () => {
    it('puts the most recently moved task first', () => {
        const sorted = sortByLastMoved([
            makeTask('old', '2025-01-01T00:00:00Z'),
            makeTask('newest', '2025-03-01T00:00:00Z'),
            makeTask('middle', '2025-02-01T00:00:00Z'),
        ]);
        expect(sorted.map(t => t.id)).toEqual(['newest', 'middle', 'old']);
    });

    it('ignores updated_at — an edit must not re-rank a card', () => {
        const a = makeTask('a', '2025-05-01T00:00:00Z');
        const b = { ...makeTask('b', '2025-01-01T00:00:00Z'), updated_at: '2026-01-01T00:00:00Z' };
        expect(sortByLastMoved([a, b]).map(t => t.id)).toEqual(['a', 'b']);
    });

    it('keeps equal timestamps in the incoming order (no tiebreaker)', () => {
        const same = '2025-01-01T00:00:00Z';
        const sorted = sortByLastMoved([
            makeTask('first', same),
            makeTask('second', same),
            makeTask('third', same),
        ]);
        expect(sorted.map(t => t.id)).toEqual(['first', 'second', 'third']);
    });

    it('sorts unparseable or missing timestamps as oldest', () => {
        const sorted = sortByLastMoved([
            makeTask('no-moved-at', '' as string),
            makeTask('bad', 'not-a-date'),
            makeTask('real', '2025-01-01T00:00:00Z'),
        ]);
        expect(sorted.map(t => t.id)).toEqual(['real', 'no-moved-at', 'bad']);
    });

    it('does not mutate the input array', () => {
        const tasks = [
            makeTask('old', '2025-01-01T00:00:00Z'),
            makeTask('new', '2025-02-01T00:00:00Z'),
        ];
        sortByLastMoved(tasks);
        expect(tasks.map(t => t.id)).toEqual(['old', 'new']);
    });

    it('handles empty array', () => {
        expect(sortByLastMoved([])).toEqual([]);
    });

    it('handles single element', () => {
        const task = makeTask('only', '2025-01-01T00:00:00Z');
        expect(sortByLastMoved([task])).toEqual([task]);
    });
});

describe('sortByLastMoved with oldestFirst', () => {
    it('puts the least recently moved task first', () => {
        const sorted = sortByLastMoved(
            [
                makeTask('newest', '2025-03-01T00:00:00Z'),
                makeTask('old', '2025-01-01T00:00:00Z'),
                makeTask('middle', '2025-02-01T00:00:00Z'),
            ],
            true,
        );
        expect(sorted.map(t => t.id)).toEqual(['old', 'middle', 'newest']);
    });

    it('treats unparseable or missing timestamps as oldest', () => {
        const sorted = sortByLastMoved(
            [
                makeTask('real', '2025-01-01T00:00:00Z'),
                makeTask('no-moved-at', '' as string),
                makeTask('bad', 'not-a-date'),
            ],
            true,
        );
        expect(sorted.map(t => t.id)).toEqual(['no-moved-at', 'bad', 'real']);
    });

    it('keeps equal timestamps in the incoming order, like the default direction', () => {
        const same = '2025-01-01T00:00:00Z';
        const input = [makeTask('first', same), makeTask('second', same), makeTask('third', same)];
        expect(sortByLastMoved(input, true).map(t => t.id)).toEqual(['first', 'second', 'third']);
        expect(sortByLastMoved(input).map(t => t.id)).toEqual(['first', 'second', 'third']);
    });

    it('does not mutate the input array', () => {
        const tasks = [
            makeTask('new', '2025-02-01T00:00:00Z'),
            makeTask('old', '2025-01-01T00:00:00Z'),
        ];
        sortByLastMoved(tasks, true);
        expect(tasks.map(t => t.id)).toEqual(['new', 'old']);
    });
});
