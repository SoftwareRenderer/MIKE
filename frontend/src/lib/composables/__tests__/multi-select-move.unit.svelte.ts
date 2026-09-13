import { describe, it, expect, vi } from 'vitest';
import { useMultiSelectActions } from '$lib/composables/use-multi-select-actions.svelte';

/** The frame the bar dispatches when a move is confirmed. */
function dispatchMove(taskIds: string[], status: string) {
    window.dispatchEvent(new CustomEvent('multi-select-move', { detail: { taskIds, status } }));
}

function wire(onMove?: (taskIds: string[], status: string) => Promise<void>) {
    const actions = useMultiSelectActions({
        onDelete: vi.fn().mockResolvedValue(undefined),
        onStart: vi.fn().mockResolvedValue(undefined),
        onMove,
    });
    return { ...actions, teardown: actions.setup() };
}

describe('useMultiSelectActions — multi-select-move', () => {
    it('hands the confirmed batch to onMove and clears the selection when it lands', async () => {
        const onMove = vi.fn().mockResolvedValue(undefined);
        const { multiSelect, teardown } = wire(onMove);
        multiSelect.selectAll(['a', 'b']);

        dispatchMove(['a', 'b'], 'review');
        await vi.waitFor(() => expect(onMove).toHaveBeenCalledWith(['a', 'b'], 'review'));
        await vi.waitFor(() => expect(multiSelect.count).toBe(0));

        teardown();
    });

    it('leaves the selection alone where the view has no move handler', async () => {
        const { multiSelect, teardown } = wire(undefined);
        multiSelect.selectAll(['a']);

        dispatchMove(['a'], 'done');
        await Promise.resolve();

        expect(multiSelect.count).toBe(1);
        teardown();
    });

    it('stops listening once the view is gone', async () => {
        const onMove = vi.fn().mockResolvedValue(undefined);
        const { teardown } = wire(onMove);

        teardown();
        dispatchMove(['a'], 'done');
        await Promise.resolve();

        expect(onMove).not.toHaveBeenCalled();
    });
});
