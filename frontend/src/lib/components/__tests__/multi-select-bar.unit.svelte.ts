import { describe, it, expect, vi, beforeEach, afterEach, type Mock } from 'vitest';
import { render, fireEvent, waitFor } from '@testing-library/svelte';
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
    appStore: {
        config: {
            providers: [
                {
                    name: 'test-provider',
                    display_name: 'Test Provider',
                    url: 'http://test:8080/v1',
                    models: [{ name: 'test-model' }],
                },
            ],
        },
    },
}));

import MultiSelectBar from '$lib/components/MultiSelectBar.svelte';
import { MultiSelect } from '$lib/composables/use-multi-select.svelte';
import { dialogStore } from '$lib/stores';
import type { Task } from '$lib/types';

/**
 * The bar a selection of cards raises. Which controls it offers is read off the selection (Start only
 * for a Backlog-only one, Move for anything spanning columns, Model while one of them is mid-turn), and
 * every control that changes the board asks first and then speaks through the one event the board
 * listens for — never by editing a row itself.
 */

function makeTask(id: string, status: string = 'backlog', parentId?: string): Task {
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

/** Frames the bar dispatched on `event`, most recent last. */
interface Recorder {
    frames: Array<Record<string, unknown>>;
    stop: () => void;
}

function recorder(event: string): Recorder {
    const frames: Array<Record<string, unknown>> = [];
    const listener = (e: Event) => frames.push((e as CustomEvent).detail);
    window.addEventListener(event, listener);
    return { frames, stop: () => window.removeEventListener(event, listener) };
}

let multiSelect: MultiSelect;
let onClear: Mock;
let board: Recorder;

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

const click = (el: Element) => el.dispatchEvent(new MouseEvent('click', { bubbles: true }));

beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(dialogStore.showConfirm).mockResolvedValue(true);
    multiSelect = new MultiSelect();
    onClear = vi.fn();
    board = recorder('multi-select-move');
});

afterEach(() => {
    board.stop();
});

describe('MultiSelectBar — the controls a selection earns', () => {
    it('offers Start for a selection that lives in Backlog', async () => {
        const { container } = renderBar([makeTask('a', 'backlog'), makeTask('b', 'backlog')]);
        multiSelect.selectAll(['a', 'b']);

        await waitFor(() => expect(container.querySelector('.bar-btn-start')).toBeTruthy());
        expect(container.querySelector('.bar-btn-start')?.textContent)
            .toContain(t('board.multiSelect.startCount', { count: 2 }));
    });

    it('offers Move, and no Start, for a selection that spans columns', async () => {
        const { container } = renderBar([makeTask('a', 'backlog'), makeTask('b', 'in_progress')]);
        multiSelect.selectAll(['a', 'b']);

        await waitFor(() => expect(container.querySelector('.bar-btn-move')).toBeTruthy());
        expect(container.querySelector('.bar-btn-start')).toBeNull();
        expect(container.querySelector('.bar-btn-model')).toBeTruthy();
    });

    it('draws no bar before a card is held, and one as soon as one is', async () => {
        const { container } = renderBar([makeTask('a', 'backlog'), makeTask('b', 'backlog')]);

        expect(container.querySelector('.multi-select-bar')).toBeNull();
        expect(container.querySelector('.bar-btn-model')).toBeNull();

        multiSelect.add('a');
        await waitFor(() => expect(container.querySelector('.multi-select-bar')).toBeTruthy());
    });

    it('shows the Model button while a selected task is mid-turn, named for the selection', () => {
        const runningTasks: Task[] = [
            makeTask('task-1', 'in_progress'),
            { ...makeTask('task-2', 'in_progress'), execution_state: 'running' },
        ];
        multiSelect.selectAll(['task-1', 'task-2']);

        const { container } = renderBar(runningTasks);

        const modelBtn = container.querySelector('.bar-btn-model');
        expect(modelBtn).toBeTruthy();
        expect(modelBtn?.textContent).toContain(t('app.model'));
        expect(modelBtn?.getAttribute('aria-label')).toBe('Change model for 2 tasks');
    });

    it('hands Clear to the caller and Delete to the board', () => {
        const deletes = recorder('multi-select-delete');
        multiSelect.selectAll(['task-1', 'task-2']);
        const { container } = renderBar([makeTask('task-1'), makeTask('task-2')]);

        click(container.querySelector('.bar-btn-clear')!);
        expect(onClear).toHaveBeenCalled();

        const deleteBtn = container.querySelector('.bar-btn-delete')!;
        expect(deleteBtn.textContent).toContain(t('board.multiSelect.deleteCount', { count: 2 }));
        click(deleteBtn);
        expect(deletes.frames).toEqual([{
            taskIds: ['task-1', 'task-2'],
            titles: ['Task task-1', 'Task task-2'],
        }]);
        deletes.stop();
    });
});

describe('MultiSelectBar — Start', () => {
    it('asks first, naming the count and the selected titles, and starts nothing until the answer is yes', async () => {
        const started = recorder('multi-select-start');
        vi.mocked(dialogStore.showConfirm).mockResolvedValue(false);
        const { container } = renderBar([makeTask('a', 'backlog'), makeTask('b', 'backlog')]);
        multiSelect.selectAll(['a', 'b']);
        await waitFor(() => expect(container.querySelector('.bar-btn-start')).toBeTruthy());

        click(container.querySelector('.bar-btn-start')!);

        await waitFor(() => expect(dialogStore.showConfirm).toHaveBeenCalled());
        expect(vi.mocked(dialogStore.showConfirm).mock.calls[0][0]).toEqual({
            title: t('board.multiSelect.startTitle'),
            message: t('board.multiSelect.startMessage', { count: 2 }),
            items: ['Task a', 'Task b'],
            confirmText: t('board.confirm.start'),
            cancelText: t('app.cancel'),
            variant: 'default',
        });
        expect(started.frames).toEqual([]);
        started.stop();
    });

    it('dispatches the confirmed start with the selection', async () => {
        const started = recorder('multi-select-start');
        const { container } = renderBar([makeTask('a', 'backlog'), makeTask('b', 'backlog')]);
        multiSelect.selectAll(['a', 'b']);
        await waitFor(() => expect(container.querySelector('.bar-btn-start')).toBeTruthy());

        click(container.querySelector('.bar-btn-start')!);

        await waitFor(() => expect(started.frames).toHaveLength(1));
        expect(started.frames[0]).toEqual({ taskIds: ['a', 'b'] });
        started.stop();
    });
});

describe('MultiSelectBar — Move to…', () => {
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
        expect(board.frames).toEqual([]);
    });

    it('dispatches the confirmed move with its column', async () => {
        const { container } = renderBar([makeTask('a', 'backlog'), makeTask('b', 'backlog')]);
        multiSelect.selectAll(['a', 'b']);
        await waitFor(() => expect(container.querySelector('.bar-btn-move')).toBeTruthy());

        await pickColumn(container, 'board.column.review');

        await waitFor(() => expect(board.frames).toHaveLength(1));
        expect(board.frames[0]).toEqual({ taskIds: ['a', 'b'], status: 'review' });
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

        await waitFor(() => expect(board.frames).toHaveLength(1));
        expect(board.frames[0]).toEqual({ taskIds: ['a'], status: 'review' });
    });

    it('reports a selection that has nowhere to go and asks nothing', async () => {
        const { container } = renderBar([makeTask('a', 'review'), makeTask('b', 'review')]);
        multiSelect.selectAll(['a', 'b']);
        await waitFor(() => expect(container.querySelector('.bar-btn-move')).toBeTruthy());

        await pickColumn(container, 'board.column.review');

        expect(dialogStore.showConfirm).not.toHaveBeenCalled();
        expect(board.frames).toEqual([]);
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

        await waitFor(() => expect(board.frames).toHaveLength(1));
        expect(board.frames[0]).toEqual({ taskIds: ['other'], status: 'backlog' });
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
        expect(onClear).not.toHaveBeenCalled();
    });
});
