import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, cleanup, fireEvent, waitFor } from '@testing-library/svelte';
import { tick } from 'svelte';
import ExplorerHeader from '$lib/components/ExplorerHeader.svelte';
import { t } from '$lib/i18n';

/**
 * The explorer's branch list, judged on the payload the branches route answers. The payload is a prop here
 * because staging one in a browser needs a database row (docs/testing.md).
 */

const HELD_TASK_BRANCH = 'task/fix-the-login-abc123';

function renderHeader(overrides: Record<string, unknown> = {}) {
    const onSelectBranch = vi.fn();
    const utils = render(ExplorerHeader, {
        props: {
            pathLabel: 'MIKE',
            projectId: 'p1',
            closeAria: 'Close explorer',
            branch: 'main',
            changesOpen: false,
            onChangesOpen: vi.fn(),
            onCloseRequest: vi.fn(),
            onOpenTerminal: vi.fn(),
            terminalHref: '/?project=p1&view=terminal',
            branches: ['main', 'feature/reconcile-caches'],
            branchesLoading: false,
            branchesError: false,
            withheld: 0,
            heldBranches: [],
            onSelectBranch,
            ...overrides,
            // eslint-disable-next-line @typescript-eslint/no-explicit-any
        } as any,
    });
    return { ...utils, onSelectBranch };
}

/** Open the list. */
async function openList(container: HTMLElement) {
    fireEvent.click(container.querySelector('.explorer-dropdown-btn') as HTMLElement);
    await tick();
}

async function queryRows(container: HTMLElement): Promise<string[]> {
    return Array.from(container.querySelectorAll('.explorer-dropdown-rows .explorer-dropdown-name'))
        .map(el => el.textContent?.trim() ?? '');
}

afterEach(cleanup);

describe('the branch list of the explorer header', () => {
    it('offers the branches this checkout could switch to, and marks the one it is on', async () => {
        const { container } = renderHeader();
        await openList(container);

        expect(await queryRows(container)).toEqual(['main', 'feature/reconcile-caches']);
        const active = container.querySelector('.explorer-dropdown-item.active');
        expect(active?.textContent).toContain('main');
    });

    it('leaves out a ref another worktree holds, and says how many it left out', async () => {
        const { container } = renderHeader({ withheld: 168 });
        await openList(container);

        const note = container.querySelector('.explorer-dropdown-note');
        expect(note?.textContent).toContain('168 branches are checked out in other worktrees');
        // A count with no reachable row must not promise one.
        expect(container.querySelector('.explorer-dropdown-hint')).toBeNull();
    });

    it('opens the task a held branch belongs to, and only lets a query find it', async () => {
        const { container } = renderHeader({
            withheld: 1,
            heldBranches: [{ name: HELD_TASK_BRANCH, taskId: 'amber-hall' }],
        });
        await openList(container);

        // The row is out of the default list, and the note says how to reach it.
        expect(container.querySelector('.explorer-dropdown-held')).toBeNull();
        expect(container.querySelector('.explorer-dropdown-hint')?.textContent).toContain('type to find one');

        fireEvent.input(container.querySelector('.explorer-dropdown-filter') as HTMLElement,
            { target: { value: 'login' } });
        await tick();

        const row = container.querySelector('.explorer-dropdown-item.is-held') as HTMLAnchorElement;
        expect(row.textContent?.trim()).toContain(HELD_TASK_BRANCH);
        expect(row.getAttribute('href')).toBe('/?project=p1&task=amber-hall');
        expect(await queryRows(container)).not.toContain(HELD_TASK_BRANCH);
    });

    it('reports a failed read instead of claiming the repository has no branches', async () => {
        const onRetryBranches = vi.fn();
        const { container } = renderHeader({ branches: [], branchesError: true, onRetryBranches });
        await openList(container);

        expect(container.textContent).toContain(t('explorer.view.errLoadBranches'));
        expect(container.textContent).not.toContain(t('explorer.view.noBranches'));

        fireEvent.click(container.querySelector('.explorer-dropdown-retry') as HTMLElement);
        expect(onRetryBranches).toHaveBeenCalledTimes(1);
    });

    it('says the repository has no branches when it truly has none, and no match when a query missed', async () => {
        const empty = renderHeader({ branches: [] });
        await openList(empty.container);
        expect(empty.container.textContent).toContain(t('explorer.view.noBranches'));
        cleanup();

        const { container } = renderHeader();
        await openList(container);
        fireEvent.input(container.querySelector('.explorer-dropdown-filter') as HTMLElement,
            { target: { value: 'nothing-matches' } });
        await tick();
        expect(container.textContent).toContain(t('explorer.view.noBranchMatch'));
        expect(container.textContent).not.toContain(t('explorer.view.noBranches'));
    });
});

describe('the keyboard in the branch list', () => {
    it('opens on ArrowDown from the trigger, and the list is a popup of the trigger', async () => {
        const { container } = renderHeader();
        const trigger = container.querySelector('.explorer-dropdown-btn') as HTMLElement;

        fireEvent.keyDown(trigger, { key: 'ArrowDown' });
        await tick();

        expect(container.querySelector('.explorer-dropdown-list')).not.toBeNull();
        expect(trigger.getAttribute('aria-expanded')).toBe('true');
        expect(trigger.getAttribute('aria-haspopup')).toBe('listbox');
        // Opening puts focus in the filter field.
        await waitFor(() => expect(document.activeElement).toBe(container.querySelector('.explorer-dropdown-filter')));
    });

    it('lands the arrows on a row, and that row is what activation switches to', async () => {
        const { container, onSelectBranch } = renderHeader();
        await openList(container);
        const list = container.querySelector('.explorer-dropdown-list') as HTMLElement;
        const rows = Array.from(container.querySelectorAll<HTMLElement>('.explorer-dropdown-rows .explorer-dropdown-item'));

        fireEvent.keyDown(list, { key: 'ArrowDown' });
        await tick();
        expect(document.activeElement).toBe(rows[0]);

        fireEvent.keyDown(list, { key: 'End' });
        await tick();
        expect(document.activeElement).toBe(rows[rows.length - 1]);

        fireEvent.keyDown(list, { key: 'Home' });
        await tick();
        expect(document.activeElement).toBe(rows[0]);

        // jsdom does not turn a keydown into a button activation, so this measures where the arrows put focus.
        fireEvent.click(document.activeElement as HTMLElement);
        expect(onSelectBranch).toHaveBeenCalledWith('main');
    });

    it('steps onto a held task row with the same arrows, because a query made it a row like any other', async () => {
        const { container } = renderHeader({
            branches: ['main'],
            withheld: 1,
            heldBranches: [{ name: HELD_TASK_BRANCH, taskId: 'amber-hall' }],
        });
        await openList(container);
        fireEvent.input(container.querySelector('.explorer-dropdown-filter') as HTMLElement,
            { target: { value: 'login' } });
        await tick();

        // No switchable row matches, so the task row is the only one.
        const list = container.querySelector('.explorer-dropdown-list') as HTMLElement;
        const held = container.querySelector('.explorer-dropdown-item.is-held') as HTMLElement;
        fireEvent.keyDown(list, { key: 'ArrowDown' });
        await tick();
        expect(document.activeElement).toBe(held);
    });

    it('returns focus to the trigger when the list closes', async () => {
        const { container } = renderHeader();
        const trigger = container.querySelector('.explorer-dropdown-btn') as HTMLElement;
        await openList(container);
        await waitFor(() => expect(document.activeElement).toBe(container.querySelector('.explorer-dropdown-filter')));

        // A click anywhere else closes the list.
        fireEvent.click(document.body);
        await waitFor(() => expect(container.querySelector('.explorer-dropdown-list')).toBeNull());
        expect(document.activeElement).toBe(trigger);
    });
});
