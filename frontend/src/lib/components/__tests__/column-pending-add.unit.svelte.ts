import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, fireEvent, cleanup } from '@testing-library/svelte';

vi.mock('svelte/transition', () => ({ slide: vi.fn(), fade: vi.fn() }));
vi.mock('$lib/stores', () => ({
    dialogStore: { showConfirm: vi.fn().mockResolvedValue(true), showToast: vi.fn() },
    uiStore: {},
    appStore: { config: { providers: [] } },
}));

import Column from '$lib/components/Column.svelte';
import { projectAddPending } from '$lib/utils/board-pending';
import type { Project } from '$lib/types';

/**
 * THE BACKLOG COLUMN'S ADD SLOT.
 *
 * The column's first row is the "Create Task" button, and while the project belongs to no server row
 * that slot belongs to the add instead. A task created against the placeholder's id names a project
 * the server has never seen, so no add affordance may survive — not the button, not the icon-only one
 * the header grows when the list scrolls.
 */

function row(overrides: Partial<Project> = {}): Project {
    return {
        id: 'clone-pending-1', name: 'repo', path: '', created_at: '', updated_at: '',
        cloning: true, ...overrides,
    } as Project;
}

function renderColumn(pending: ReturnType<typeof projectAddPending>, onAddTask = vi.fn()) {
    return render(Column, {
        columnId: 'backlog',
        label: 'Backlog',
        color: 'var(--color-column-backlog)',
        tasks: [],
        projectId: 'clone-pending-1',
        childrenMap: new Map(),
        expandedTasks: new Set<string>(),
        onToggleExpanded: vi.fn(),
        onDrop: vi.fn(),
        onAddTask,
        pending,
        onDismissPending: vi.fn(),
    });
}

const bar = (container: HTMLElement) => container.querySelector('[role="progressbar"]');
const caption = (container: HTMLElement) => container.querySelector('.pending-caption');

afterEach(cleanup);

describe('the backlog column while its project is being added', () => {
    it('trades the add-task button for the bar of the add in flight', () => {
        const { container } = renderColumn(projectAddPending(row({ cloneProgress: 42, cloneStatus: 'Receiving objects...' })));

        expect(container.querySelector('.add-task-btn')).toBeNull();
        expect(bar(container)).not.toBeNull();
        expect(bar(container)?.getAttribute('aria-valuenow')).toBe('42');
        expect(caption(container)?.textContent).toBe('Receiving objects...');
    });

    it('hides the header add icon the scrolled list would otherwise grow', async () => {
        const withButton = renderColumn(null);
        const list = withButton.container.querySelector('.card-list') as HTMLElement;
        list.scrollTop = 40;
        await fireEvent.scroll(list);
        expect(withButton.container.querySelector('.add-btn')).not.toBeNull();

        const pending = renderColumn(projectAddPending(row()));
        const pendingList = pending.container.querySelector('.card-list') as HTMLElement;
        pendingList.scrollTop = 40;
        await fireEvent.scroll(pendingList);
        expect(pending.container.querySelector('.add-btn')).toBeNull();
    });

    it('holds what git said when the add stopped, and the one way out of it', async () => {
        const onDismiss = vi.fn();
        const { container } = render(
            Column,
            {
                columnId: 'backlog', label: 'Backlog', color: 'blue', tasks: [], projectId: 'x',
                childrenMap: new Map(), expandedTasks: new Set<string>(), onToggleExpanded: vi.fn(),
                onDrop: vi.fn(), onAddTask: vi.fn(), onDismissPending: onDismiss,
                pending: projectAddPending(row({
                    cloneProgress: 60,
                    cloneFailure: { message: 'git clone failed', detail: 'fatal: no such repository' },
                })),
            },
        );

        expect(container.querySelector('.add-task-btn')).toBeNull();
        expect(bar(container)?.getAttribute('aria-valuenow')).toBe('60');
        expect(caption(container)?.textContent).toBe('git clone failed');
        expect(container.querySelector('.pending-detail')?.textContent).toBe('fatal: no such repository');

        const dismiss = Array.from(container.querySelectorAll('button'))
            .find((b) => b.textContent?.trim() === 'Dismiss');
        expect(dismiss).toBeDefined();
        await fireEvent.click(dismiss as HTMLElement);
        expect(onDismiss).toHaveBeenCalled();
    });

    it('gives the slot back to the add-task button once the row exists', async () => {
        const onAddTask = vi.fn();
        const { container, rerender } = renderColumn(projectAddPending(row({ cloneProgress: 10 })), onAddTask);
        expect(container.querySelector('.add-task-btn')).toBeNull();

        await rerender({ pending: null });
        expect(bar(container)).toBeNull();
        const button = container.querySelector('.add-task-btn') as HTMLButtonElement;
        expect(button).not.toBeNull();
        await fireEvent.click(button);
        expect(onAddTask).toHaveBeenCalled();
    });
});
