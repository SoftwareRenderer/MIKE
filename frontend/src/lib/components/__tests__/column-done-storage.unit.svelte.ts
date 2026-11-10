import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, fireEvent, cleanup } from '@testing-library/svelte';
import { t } from '$lib/i18n';

vi.mock('svelte/transition', () => ({ slide: vi.fn(), fade: vi.fn() }));
vi.mock('$lib/stores', () => ({
    dialogStore: { showConfirm: vi.fn().mockResolvedValue(true), showToast: vi.fn() },
    uiStore: {},
    appStore: { config: { providers: [] } },
}));

import Column from '$lib/components/Column.svelte';
import { storageStatus } from '$lib/services/storage-usage.svelte';

/** One column, no tasks, and only the reading under test behind it. */
function renderColumn(columnId: string) {
    return render(Column, {
        columnId,
        label: columnId,
        color: 'blue',
        tasks: [],
        projectId: 'p1',
        childrenMap: new Map(),
        expandedTasks: new Set<string>(),
        onToggleExpanded: vi.fn(),
        onCreateChildTask: vi.fn(),
        onRefreshBoard: vi.fn(),
        onDrop: vi.fn(),
    });
}

/** A reading of `used` of `total` bytes. */
function withReading(used: number, total: number): void {
    storageStatus.update({ total_bytes: total, avail_bytes: total - used });
}

function withoutReading(): void {
    withReading(0, 0);
}

/** The reading after the server walked the worktrees: `bytes` across `count` of them. */
function withTaskFigure(bytes: number, count: number): void {
    storageStatus.update({ total_bytes: 1024, avail_bytes: 1024 - 430, tasks_bytes: bytes, tasks_count: count });
}

const tooltip = (container: HTMLElement) => container.querySelector('[role="tooltip"]');

const count = (container: HTMLElement) => container.querySelector('.header-right .count');

beforeEach(() => {
    withoutReading();
});

afterEach(() => {
    cleanup();
});

describe('the Done column count as the filesystem reading', () => {
    it('is a control only on Done, and only once the server has said something', () => {
        withReading(430, 1024);
        const done = renderColumn('done');
        expect(done.container.querySelector('button.storage-count')).toBeTruthy();
        cleanup();

        const backlog = renderColumn('backlog');
        expect(backlog.container.querySelector('button.storage-count')).toBeNull();
        expect(count(backlog.container)?.tagName).toBe('SPAN');
    });

    it('stays the plain count it was before any reading arrived', () => {
        const { container } = renderColumn('done');
        expect(container.querySelector('button.storage-count')).toBeNull();
        expect(count(container)?.tagName).toBe('SPAN');
    });

    // jsdom has no hover — the case a tap has to cover.
    it('reveals the percentage to a tap, and hides it from the next one', async () => {
        withReading(430, 1024);
        const { container } = renderColumn('done');
        const badge = count(container) as HTMLElement;

        expect(container.querySelector('[role="tooltip"]')).toBeNull();
        expect(badge.getAttribute('aria-expanded')).toBe('false');

        await fireEvent.click(badge);
        const tip = container.querySelector('[role="tooltip"]');
        expect(tip?.textContent).toContain('42%');
        expect(badge.getAttribute('aria-expanded')).toBe('true');
        expect(badge.getAttribute('aria-describedby')).toBe(tip?.getAttribute('id'));

        await fireEvent.click(badge);
        expect(container.querySelector('[role="tooltip"]')).toBeNull();
        expect(badge.getAttribute('aria-describedby')).toBeNull();
    });

    it('closes on Escape, the keyboard way out of a pinned reading', async () => {
        withReading(430, 1024);
        const { container } = renderColumn('done');
        const badge = count(container) as HTMLElement;

        await fireEvent.click(badge);
        expect(container.querySelector('[role="tooltip"]')).toBeTruthy();
        await fireEvent.keyDown(badge, { key: 'Escape' });
        expect(container.querySelector('[role="tooltip"]')).toBeNull();
    });

    it('names itself differently once the disk is nearly full', () => {
        withReading(900, 1024);
        const { container } = renderColumn('done');
        expect(count(container)?.getAttribute('aria-label')).toBe(t('board.column.storageLowAria'));
    });

    it('warns with a symbol and the warning colour past four fifths, and with neither below', () => {
        withReading(900, 1024);
        const low = renderColumn('done');
        const lowBadge = count(low.container) as HTMLElement;
        expect(lowBadge.className).toContain('is-low');
        expect(lowBadge.querySelector('svg')).toBeTruthy();
        cleanup();

        withReading(430, 1024);
        const calm = renderColumn('done');
        const calmBadge = count(calm.container) as HTMLElement;
        expect(calmBadge.className).not.toContain('is-low');
        expect(calmBadge.querySelector('svg')).toBeNull();
    });
});

describe('the worktree figure inside the disclosure', () => {
    it('is a second line under the reading, once the server has measured one', async () => {
        withTaskFigure(12 * 1024 ** 3, 8);
        const { container } = renderColumn('done');
        await fireEvent.click(count(container) as HTMLElement);

        const tip = tooltip(container);
        expect(tip?.textContent).toContain('42%');
        expect(tip?.textContent).toContain('Tasks: 12 GB in 8 worktrees');
        expect(tip?.querySelectorAll('div').length).toBe(2);
    });

    // The walk lags the filesystem reading by the server's budget, and a refused walk never fills it
    // in: the line is absent, never 0 B in 0 worktrees.
    it('is absent while the worktrees are unmeasured', async () => {
        withReading(430, 1024);
        const { container } = renderColumn('done');
        await fireEvent.click(count(container) as HTMLElement);

        const tip = tooltip(container);
        expect(tip?.textContent).toContain('42%');
        expect(tip?.textContent).not.toContain('Tasks:');
        expect(tip?.querySelectorAll('div').length).toBe(1);
    });

    it('says one worktree when the figure holds one', async () => {
        withTaskFigure(512, 1);
        const { container } = renderColumn('done');
        await fireEvent.click(count(container) as HTMLElement);

        expect(tooltip(container)?.textContent).toContain('Tasks: 512 B in 1 worktree');
    });
});
