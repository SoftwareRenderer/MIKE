import { describe, it, expect, beforeEach } from 'vitest';
import { t, resetToEnglish } from '$lib/i18n';
import { en, type MessageKey } from '$lib/i18n/en';

/**
 * The board + task view batch's copy, asserted at the catalog boundary:
 *
 *  - every key the batch owns resolves to English (never to its own key), and the
 *    English is **byte-identical to the literal that used to sit in the component**,
 *  - plurals ride `Intl.PluralRules` through `t(…, { count })`, so no component has
 *    to keep a `count === 1 ? 's' : ''` of its own,
 *  - interpolation is named, so a sentence survives a locale that reorders its parts.
 *
 * `aria-label`/`title` text is deliberately absent: it is not translated in this app,
 * and the component tests that assert it (card-action-padding, link-rows,
 * task-header-sandbox-dot, …) stay anchored on the English literals.
 */

/** Every key this batch appended to the catalog of record. */
function batchKeys(): MessageKey[] {
    return (Object.keys(en) as MessageKey[]).filter(k => k.startsWith('board.') || k.startsWith('task.'));
}

describe('board + task view catalog', () => {
    beforeEach(() => resetToEnglish());

    it('owns a section of the catalog', () => {
        // Guards against a half-migrated batch: keys are expected, and a rename that
        // drops them must fail here rather than print keys on screen.
        expect(batchKeys().length).toBeGreaterThan(60);
    });

    it('answers every key with English instead of the key itself', () => {
        for (const key of batchKeys()) {
            const value = t(key, { count: 2 });
            expect(value, key).not.toBe(key);
            expect(value.length, key).toBeGreaterThan(0);
        }
    });

    it('keeps the English copy byte-identical to the components it came from', () => {
        expect(t('board.column.backlog')).toBe('Backlog');
        expect(t('board.column.inProgress')).toBe('In Progress');
        expect(t('board.column.review')).toBe('Review');
        expect(t('board.column.done')).toBe('Done');
        expect(t('board.column.addTask')).toBe('+ Create Task');

        expect(t('board.toast.moved', { status: t('board.column.review') })).toBe('Task moved to Review');
        expect(t('board.toast.moveFailed', { error: 'boom' })).toBe('Failed to move task: boom');
        expect(
            t('board.toast.cannotMoveToStatus', {
                targetStatus: t('board.column.backlog'),
                parentStatus: t('board.column.inProgress'),
            }),
        ).toBe('Cannot move to Backlog: parent task is In Progress');
        expect(t('board.deleteTasks.title')).toBe('Delete Tasks');
        expect(t('board.clearDone.title')).toBe('Clear Done Tasks');
        expect(t('board.clearDone.failed', { error: 'boom' })).toBe('Failed to clear done tasks: boom');

        expect(t('board.card.commitAndDone')).toBe('Commit and move to Done');
        expect(t('board.deleteTask.title')).toBe('Delete Task');
        expect(t('board.deleteTask.message')).toBe('Delete this task? This cannot be undone.');
        expect(t('board.card.modelWithProvider', { provider: 'OpenAI', model: 'gpt-mini' })).toBe('OpenAI · gpt-mini');
        expect(t('board.untitled')).toBe('(untitled)');

        expect(t('board.editTask.title')).toBe('Edit Task');
        expect(t('board.editTask.branchDesc')).toBe("Base branch for this task's changes");
        expect(t('board.editTask.autoMergeInto', { branch: 'main' })).toBe('Merge changes into main when complete');
        expect(t('board.editTask.autoMergeIntoParent')).toBe('Merge changes into parent task when complete');
        expect(t('board.editTask.subtasksAutoMergeDesc')).toBe('Subtasks created under this task will auto-merge into it');

        expect(t('task.noChanges')).toBe('No changes yet');
        expect(t('task.diff.mergedFromSubtasks')).toBe('Merged from Subtasks');
        expect(t('task.diff.committedInTask')).toBe('Committed in task');
        expect(t('task.diff.merged')).toBe('Merged');
        expect(t('task.diff.workingTree')).toBe('Working tree');
        expect(t('task.diff.originalBranch')).toBe('Original branch');
        expect(t('task.diff.originMeta', { hash: 'a1b2c3d' })).toBe('a1b2c3d (Origin)');
        expect(t('task.checkpoint.title')).toBe('Changes up to this checkpoint');
        expect(t('task.graph.history')).toBe('History');
        expect(t('task.graph.empty')).toBe('No commits');
    });

    it('answers the plurals the board used to spell out by hand', () => {
        // `${n} task${n > 1 ? 's' : ''}` and friends — now one plural table each.
        expect(t('board.toast.started', { count: 1 })).toBe('1 task started');
        expect(t('board.toast.started', { count: 3 })).toBe('3 tasks started');
        expect(t('board.toast.modelUpdated', { count: 1 })).toBe('Model updated for 1 task');
        expect(t('board.toast.modelUpdated', { count: 2 })).toBe('Model updated for 2 tasks');
        expect(t('board.clearDone.deleted', { count: 1 })).toBe('1 done task deleted');
        expect(t('board.clearDone.deleted', { count: 0 })).toBe('0 done tasks deleted');
        expect(t('board.multiSelect.startCount', { count: 1 })).toBe('Start 1 task');
        expect(t('board.multiSelect.deleteCount', { count: 4 })).toBe('Delete 4 tasks');
        expect(t('board.card.fileCount', { count: 1 })).toBe('1 file');
        expect(t('board.card.fileCount', { count: 12 })).toBe('12 files');
        expect(t('board.card.subtaskCount', { count: 1 })).toBe('1 subtask');
        expect(t('board.card.subtaskCount', { count: 2 })).toBe('2 subtasks');
    });

    it('selects plural forms through Intl.PluralRules, including zero and fractions', () => {
        // `count === 1` would answer "1 done task" for 1 and for nothing else; the
        // rules answer with the `other` form for 0 and for fractions, which is what
        // English says and what every other language needs its own table for.
        expect(t('board.deleteTasks.message', { count: 1 })).toBe('Permanently delete 1 task? This cannot be undone.');
        expect(t('board.deleteTasks.message', { count: 0 })).toBe('Permanently delete 0 tasks? This cannot be undone.');
        expect(t('board.multiSelect.startMessage', { count: 1.5 })).toBe('Start 1.5 tasks?');
    });

    it('leaves a placeholder nobody filled in visible, not empty', () => {
        // A missing param is a bug in the call site; hiding it would hide the bug.
        expect(t('board.toast.deleteFailed')).toBe('Failed to delete task: {error}');
    });

    it('keeps status ids as data and labels as messages', () => {
        // The ids are compared, persisted and sent to the API; only labels translate.
        const ids = ['backlog', 'in_progress', 'review', 'done'] as const;
        const labels = ids.map(id => t(`board.column.${id === 'in_progress' ? 'inProgress' : id}` as MessageKey));
        expect(ids).toEqual(['backlog', 'in_progress', 'review', 'done']);
        expect(labels).toEqual(['Backlog', 'In Progress', 'Review', 'Done']);
    });
});
