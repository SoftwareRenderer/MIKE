import { describe, it, expect } from 'vitest';
import { subtaskStatusFor, subtaskExecutionStateFor } from '$lib/utils/subtask-status';

// These mirror types.SubtaskStatusFor (Go), which decides the real thing. The frontend
// copy exists so the optimistic card lands in the column the server will put it in —
// a wrong answer here is a card that appears in Backlog and jumps to In Progress when
// the create lands, or a Pending card where the server created a backlog one.
describe('subtaskStatusFor — where a new subtask lands', () => {
    it('places a subtask of a started task beside its parent', () => {
        expect(subtaskStatusFor('in_progress')).toBe('in_progress');
    });

    it('places a subtask of a not-yet-started or finished task in the backlog', () => {
        expect(subtaskStatusFor('backlog')).toBe('backlog');
        expect(subtaskStatusFor('review')).toBe('backlog');
        expect(subtaskStatusFor('done')).toBe('backlog');
    });

    it('falls back to the backlog when the parent status is unknown', () => {
        // A subtask created offline never fetched its parent, and any status the
        // server does not know has to be an answer that is still a real column.
        expect(subtaskStatusFor(undefined)).toBe('backlog');
        expect(subtaskStatusFor('')).toBe('backlog');
        expect(subtaskStatusFor('whatever')).toBe('backlog');
    });
});

describe('subtaskExecutionStateFor — the badge that goes with it', () => {
    it('reads Pending (idle) in the started column', () => {
        expect(subtaskExecutionStateFor('in_progress')).toBe('idle');
    });

    it('carries no execution state in the backlog, as a never-run task does', () => {
        expect(subtaskExecutionStateFor('backlog')).toBe('');
        expect(subtaskExecutionStateFor('done')).toBe('');
        expect(subtaskExecutionStateFor(undefined)).toBe('');
    });
});
