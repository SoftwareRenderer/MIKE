import { describe, it, expect, vi, beforeEach, afterEach, type Mock } from 'vitest';
import { render, fireEvent, waitFor, cleanup } from '@testing-library/svelte';
import { t } from '$lib/i18n';

vi.mock('svelte/transition', () => ({
    slide: vi.fn(),
    fade: vi.fn(),
}));

vi.mock('$lib/stores', () => ({
    dialogStore: {
        showConfirm: vi.fn().mockResolvedValue(true),
        showToast: vi.fn(),
    },
    uiStore: {},
    appStore: { config: { providers: [] } },
}));

import MultiSelectBar from '$lib/components/MultiSelectBar.svelte';
import { MultiSelect } from '$lib/composables/use-multi-select.svelte';
import { dialogStore } from '$lib/stores';
import type { Task } from '$lib/types';

function makeTask(id: string, status: string, parentId?: string): Task {
    return {
        id,
        project_id: 'proj-1',
        title: `Task ${id}`,
        prompt: '',
        status,
        execution_state: 'idle',
        has_merge_conflict: false,
        max_consecutive_failed_turns: 0,
        auto_commit: true,
        auto_merge: true,
        subtasks_auto_merge: true,
        operating_mode: 'act',
        title_generating: false,
        summarizing: false,
        created_at: new Date().toISOString(),
        updated_at: new Date().toISOString(),
        moved_at: new Date().toISOString(),
        ...(parentId ? { parent_task_id: parentId } : {}),
    };
}

/** Frames the bar dispatched on `multi-select-move`, most recent last. */
let dispatched: Array<{ taskIds: string[]; status: string }> = [];
const moveListener = (e: Event) => dispatched.push((e as CustomEvent).detail);

let multiSelect: MultiSelect;
let onClear: Mock;

function renderBar(tasks: Task[]) {
    return render(MultiSelectBar, { multiSelect, tasks, onClear });
}

/** Picks a column by its visible label. */
async function pickColumn(container: HTMLElement, labelKey: 'board.column.backlog' | 'board.column.inProgress' | 'board.column.review' | 'board.column.done') {
    await fireEvent.click(container.querySelector('.bar-btn-move')!);
    const label = t(labelKey);
    const item = [...container.querySelectorAll('.move-item')].find(el => el.textContent?.trim() === label);
    expect(item, `no move item labelled ${label}`).toBeTruthy();
    await fireEvent.click(item!);
}

beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(dialogStore.showConfirm).mockResolvedValue(true);
    multiSelect = new MultiSelect();
    onClear = vi.fn();
    dispatched = [];
    window.addEventListener('multi-select-move', moveListener);
});

afterEach(() => {
    window.removeEventListener('multi-select-move', moveListener);
    cleanup();
});

describe('MultiSelectBar — Move to…', () => {
    it('offers the move for a selection that spans columns, where Start and Model are absent', async () => {
        const { container } = renderBar([makeTask('a', 'backlog'), makeTask('b', 'in_progress')]);
        multiSelect.selectAll(['a', 'b']);

        await waitFor(() => expect(container.querySelector('.bar-btn-move')).toBeTruthy());
        expect(container.querySelector('.bar-btn-start')).toBeNull();
        expect(container.querySelector('.bar-btn-model')).toBeNull();
    });

    it('lists the four board columns', async () => {
        const { container } = renderBar([makeTask('a', 'backlog')]);
        multiSelect.add('a');
        await waitFor(() => expect(container.querySelector('.bar-btn-move')).toBeTruthy());

        await fireEvent.click(container.querySelector('.bar-btn-move')!);

        const labels = [...container.querySelectorAll('.move-item')].map(el => el.textContent?.trim());
        expect(labels).toEqual([
            t('board.column.backlog'),
            t('board.column.inProgress'),
            t('board.column.review'),
            t('board.column.done'),
        ]);
    });

    it('asks first, and moves nothing until the answer is yes', async () => {
        vi.mocked(dialogStore.showConfirm).mockResolvedValue(false);
        const { container } = renderBar([makeTask('a', 'backlog'), makeTask('b', 'backlog')]);
        multiSelect.selectAll(['a', 'b']);
        await waitFor(() => expect(container.querySelector('.bar-btn-move')).toBeTruthy());

        await pickColumn(container, 'board.column.done');

        await waitFor(() => expect(dialogStore.showConfirm).toHaveBeenCalled());
        expect(dispatched).toEqual([]);
    });

    it('dispatches the confirmed move with its column', async () => {
        const { container } = renderBar([makeTask('a', 'backlog'), makeTask('b', 'backlog')]);
        multiSelect.selectAll(['a', 'b']);
        await waitFor(() => expect(container.querySelector('.bar-btn-move')).toBeTruthy());

        await pickColumn(container, 'board.column.review');

        await waitFor(() => expect(dispatched).toHaveLength(1));
        expect(dispatched[0]).toEqual({ taskIds: ['a', 'b'], status: 'review' });
        expect(vi.mocked(dialogStore.showConfirm).mock.calls[0][0]).toMatchObject({
            confirmText: t('board.confirm.move'),
            variant: 'default',
        });
    });

    it('says what moving to In Progress does', async () => {
        const { container } = renderBar([makeTask('a', 'backlog'), makeTask('b', 'backlog')]);
        multiSelect.selectAll(['a', 'b']);
        await waitFor(() => expect(container.querySelector('.bar-btn-move')).toBeTruthy());

        await pickColumn(container, 'board.column.inProgress');

        await waitFor(() => expect(dialogStore.showConfirm).toHaveBeenCalled());
        expect(vi.mocked(dialogStore.showConfirm).mock.calls[0][0].message).toBe(
            t('board.multiSelect.moveStartsMessage', { count: 2, status: t('board.column.inProgress') })
        );
    });

    it('leaves a task that is already in the target column out of the move', async () => {
        const { container } = renderBar([makeTask('a', 'backlog'), makeTask('b', 'review')]);
        multiSelect.selectAll(['a', 'b']);
        await waitFor(() => expect(container.querySelector('.bar-btn-move')).toBeTruthy());

        await pickColumn(container, 'board.column.review');

        await waitFor(() => expect(dispatched).toHaveLength(1));
        expect(dispatched[0]).toEqual({ taskIds: ['a'], status: 'review' });
    });

    it('reports a selection that has nowhere to go and asks nothing', async () => {
        const { container } = renderBar([makeTask('a', 'review'), makeTask('b', 'review')]);
        multiSelect.selectAll(['a', 'b']);
        await waitFor(() => expect(container.querySelector('.bar-btn-move')).toBeTruthy());

        await pickColumn(container, 'board.column.review');

        expect(dialogStore.showConfirm).not.toHaveBeenCalled();
        expect(dispatched).toEqual([]);
        expect(dialogStore.showToast).toHaveBeenCalledWith(
            t('board.multiSelect.moveAlreadyThere', { count: 2, status: t('board.column.review') }),
            'error',
        );
    });

    it('names the tasks the server would refuse instead of sending them', async () => {
        // `child` sits under an In Progress parent that is not selected, so Backlog is the move the
        // backend refuses; `other` is unaffected.
        const { container } = renderBar([
            makeTask('parent', 'in_progress'),
            makeTask('child', 'done', 'parent'),
            makeTask('other', 'review'),
        ]);
        multiSelect.selectAll(['child', 'other']);
        await waitFor(() => expect(container.querySelector('.bar-btn-move')).toBeTruthy());

        await pickColumn(container, 'board.column.backlog');

        await waitFor(() => expect(dispatched).toHaveLength(1));
        expect(dispatched[0]).toEqual({ taskIds: ['other'], status: 'backlog' });
        expect(dialogStore.showToast).toHaveBeenCalledWith(
            t('board.toast.cannotMoveToStatus', {
                targetStatus: t('board.column.backlog'),
                parentStatus: t('board.column.inProgress'),
            }),
            'error',
        );
    });

    it('gives Escape to the menu, not to the selection-clearing handler above it', async () => {
        const { container } = renderBar([makeTask('a', 'backlog')]);
        multiSelect.add('a');
        await waitFor(() => expect(container.querySelector('.bar-btn-move')).toBeTruthy());

        const trigger = container.querySelector<HTMLButtonElement>('.bar-btn-move')!;
        await fireEvent.click(trigger);
        expect(container.querySelector('.move-menu')).toBeTruthy();

        const sawEscape = vi.fn();
        window.addEventListener('keydown', sawEscape);
        await fireEvent.keyDown(trigger, { key: 'Escape' });
        window.removeEventListener('keydown', sawEscape);

        expect(container.querySelector('.move-menu')).toBeNull();
        expect(sawEscape).not.toHaveBeenCalled();
    });
});
