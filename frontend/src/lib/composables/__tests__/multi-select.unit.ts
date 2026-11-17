import { describe, it, expect } from 'vitest';
import { MultiSelect } from '$lib/composables/use-multi-select.svelte';

/**
 * The board's multi-select is a `Set` a `$derived` reader depends on, so the one rule that
 * makes it a class rather than a `Set` is that every change hands out a **new** `Set` —
 * Svelte sees the new reference and not an in-place edit. The algebra below is what the
 * board's bulk actions ask for; the identity assertions are what makes them repaint.
 */

/** `[label, calls in order, the selection each leaves]` */
const CASES: Array<[string, string[], string[]]> = [
    ['one toggle in', ['toggle:task-1'], ['task-1']],
    ['a second task joins', ['toggle:task-1', 'toggle:task-2'], ['task-1', 'task-2']],
    ['toggling again takes it out', ['toggle:task-1', 'toggle:task-2', 'toggle:task-1'], ['task-2']],
    ['add keeps what is already selected', ['toggle:task-1', 'add:task-2'], ['task-1', 'task-2']],
    ['add does not duplicate', ['add:task-1', 'add:task-1'], ['task-1']],
    ['remove takes one out and leaves the others', ['toggle:a', 'toggle:b', 'toggle:c', 'remove:b'], ['a', 'c']],
    ['remove of an unselected task changes nothing', ['toggle:task-1', 'remove:absent'], ['task-1']],
];

describe('MultiSelect', () => {
    const run = (ms: MultiSelect, call: string): void => {
        const [method, id] = call.split(':') as ['toggle' | 'add' | 'remove', string];
        ms[method](id);
    };

    it('starts with nothing selected', () => {
        const ms = new MultiSelect();
        expect(ms.count).toBe(0);
        expect(ms.hasSelection).toBe(false);
        expect(ms.toArray()).toEqual([]);
    });

    it.each(CASES)('%s', (_label, calls, expected) => {
        const ms = new MultiSelect();
        for (const call of calls) run(ms, call);

        expect([...ms.selectedIds].sort()).toEqual([...expected].sort());
        expect(ms.count).toBe(expected.length);
        expect(ms.hasSelection).toBe(expected.length > 0);
        expect(ms.toArray().sort()).toEqual([...expected].sort());
        for (const id of expected) expect(ms.isSelected(id)).toBe(true);
    });

    it('selects all by replacing what was selected, and clears by emptying', () => {
        const ms = new MultiSelect();
        ms.toggle('task-1');

        ms.selectAll(['task-a', 'task-b', 'task-a']);
        expect([...ms.selectedIds].sort()).toEqual(['task-a', 'task-b']);
        expect(ms.isSelected('task-1')).toBe(false);

        ms.clear();
        expect(ms.count).toBe(0);
        expect(ms.hasSelection).toBe(false);
    });

    it('leaves the set a reader is holding alone when the selection changes', () => {
        // The board keeps the selection in `$state`, so a change must hand out a new `Set`:
        // Svelte sees the new reference and not an in-place edit, and a component that read
        // the selection before the change must still be looking at what it read.
        const ms = new MultiSelect();
        ms.toggle('task-1');
        const held = ms.selectedIds;

        ms.toggle('task-2');
        ms.remove('task-1');

        expect([...held]).toEqual(['task-1']);
        expect([...ms.selectedIds]).toEqual(['task-2']);
    });
});
