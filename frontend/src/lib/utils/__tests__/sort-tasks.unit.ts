import { describe, it, expect } from 'vitest';
import { sortByActivity } from '$lib/utils/sort-tasks';

function makeTask(updatedAt: string, columnOrder: number): ReturnType<typeof sortByActivity>[number] {
    return {
        id: updatedAt,
        project_id: 'proj',
        title: 'test',
        prompt: 'test',
        status: 'backlog',
        column_order: columnOrder,
        execution_state: 'idle',
        has_merge_conflict: false,
        max_consecutive_failed_turns: 0,
        auto_commit: false,
        auto_merge: false,
        subtasks_auto_merge: false,
        operating_mode: 'act',
        title_generating: false,
        created_at: '2025-01-01T00:00:00Z',
        updated_at: updatedAt,
    };
}

describe('sortByActivity', () => {
    it('sorts oldest updated_at first', () => {
        const tasks = [
            makeTask('2025-01-03T00:00:00Z', 3),
            makeTask('2025-01-01T00:00:00Z', 1),
            makeTask('2025-01-02T00:00:00Z', 2),
        ];
        const sorted = sortByActivity(tasks);
        expect(sorted.map(t => t.id)).toEqual([
            '2025-01-01T00:00:00Z',
            '2025-01-02T00:00:00Z',
            '2025-01-03T00:00:00Z',
        ]);
    });

    it('uses column_order as tiebreaker for equal timestamps', () => {
        const tasks = [
            makeTask('2025-01-01T00:00:00Z', 30),
            makeTask('2025-01-01T00:00:00Z', 10),
            makeTask('2025-01-01T00:00:00Z', 20),
        ];
        const sorted = sortByActivity(tasks);
        expect(sorted.map(t => t.column_order)).toEqual([10, 20, 30]);
    });

    it('handles empty array', () => {
        expect(sortByActivity([])).toEqual([]);
    });

    it('handles single element', () => {
        const task = makeTask('2025-01-01T00:00:00Z', 1);
        const sorted = sortByActivity([task]);
        expect(sorted).toEqual([task]);
    });
});
