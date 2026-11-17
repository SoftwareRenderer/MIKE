import { describe, it, expect, beforeEach } from 'vitest';
import { t, resetToEnglish } from '$lib/i18n';
import { en, type MessageKey } from '$lib/i18n/en';

/**
 * The board + task view's copy, asserted at the catalog boundary:
 *
 *  - every key the batch owns resolves to English rather than to its own key — a rename
 *    that drops a key prints the key on the board, and `t()`'s typed argument cannot
 *    catch a cast or a pack template;
 *  - plurals ride `Intl.PluralRules` through `t(…, { count })`, so no component has to
 *    keep a `count === 1 ? 's' : ''` of its own.
 *
 * The words themselves are catalog data, not behaviour: what is pinned here is that the
 * board's keys resolve and that its counts are selected by the rules. Interpolation and
 * the missing-placeholder case belong to the runtime's own suite.
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

    it('counts through Intl.PluralRules, including zero and fractions', () => {
        // `${n} task${n > 1 ? 's' : ''}` and friends — now one plural table each, and the
        // rules answer with the `other` form for 0 and for fractions, which is what English
        // says and what every other language needs its own table for. A `count === 1` test
        // would have made all three of those singular.
        expect(t('board.toast.started', { count: 1 })).toBe('1 task started');
        expect(t('board.toast.started', { count: 3 })).toBe('3 tasks started');
        expect(t('board.clearDone.deleted', { count: 0 })).toBe('0 done tasks deleted');
        expect(t('board.deleteTasks.message', { count: 1 }))
            .toBe('Permanently delete 1 task? This cannot be undone.');
        expect(t('board.deleteTasks.message', { count: 0 }))
            .toBe('Permanently delete 0 tasks? This cannot be undone.');
        expect(t('board.multiSelect.startMessage', { count: 1.5 })).toBe('Start 1.5 tasks?');
        expect(t('board.card.fileCount', { count: 12 })).toBe('12 files');
        expect(t('board.card.subtaskCount', { count: 2 })).toBe('2 subtasks');
    });
});
