import { describe, it, expect } from 'vitest';
import { subtaskStatusFor, subtaskExecutionStateFor, createStatusLabel } from '$lib/utils/subtask-status';
import { t, type MessageKey } from '$lib/i18n';

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

/** The board's four columns, as the board itself declares them. */
const boardColumns = [
    { id: 'backlog', labelKey: 'board.column.backlog' },
    { id: 'in_progress', labelKey: 'board.column.inProgress' },
    { id: 'review', labelKey: 'board.column.review' },
    { id: 'done', labelKey: 'board.column.done' },
] as const;

describe('createStatusLabel — the word a status is shown with', () => {
    it('names a known id with its own column heading', () => {
        const statusLabel = createStatusLabel(boardColumns);
        expect(statusLabel('backlog')).toBe(t('board.column.backlog'));
        expect(statusLabel('in_progress')).toBe(t('board.column.inProgress'));
        expect(statusLabel('review')).toBe(t('board.column.review'));
        expect(statusLabel('done')).toBe(t('board.column.done'));
    });

    it('echoes an id the table does not know instead of naming a column for it', () => {
        // Guessing a column for an id the view does not have would say the wrong thing in every
        // locale; the id itself is at least what the server sent.
        const statusLabel = createStatusLabel(boardColumns);
        expect(statusLabel('archived')).toBe('archived');
        expect(statusLabel('archived')).not.toBe(t('board.column.done'));
        expect(statusLabel('')).toBe('');
    });

    it('answers from the table it was handed', () => {
        // The task view labels the same four ids from its own section table, and a view with other
        // ids gets its own words rather than the board's.
        const sections = createStatusLabel([{ id: 'todo', labelKey: 'board.column.review' satisfies MessageKey }]);
        expect(sections('todo')).toBe(t('board.column.review'));
        expect(sections('backlog')).toBe('backlog');
    });
});
