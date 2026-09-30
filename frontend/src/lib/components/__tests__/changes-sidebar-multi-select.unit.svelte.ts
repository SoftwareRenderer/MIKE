import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { cleanup, fireEvent, waitFor } from '@testing-library/svelte';

// The global setup no-ops onMount; the panel loads its data in one.
vi.mock('svelte', async (importOriginal) => {
    const actual = await importOriginal<typeof import('svelte')>();
    return {
        ...actual,
        onMount: (cb: () => void | Promise<void>) => {
            void Promise.resolve().then(() => cb());
        },
    };
});

vi.mock('$lib/api/git', () => ({
    getGitStatus: vi.fn(),
    getProjectDiffFiles: vi.fn(),
    getProjectDiffFile: vi.fn(),
    stageProjectPaths: vi.fn(),
    discardProjectPaths: vi.fn(),
    commitProject: vi.fn(),
    pullProject: vi.fn(),
    pushProject: vi.fn(),
    getGitLogFull: vi.fn(),
    getCommitDiffFiles: vi.fn(),
}));

vi.mock('$lib/stores', () => ({
    dialogStore: {
        showToast: vi.fn(),
        showCommitDialog: vi.fn(async () => null),
        showConfirm: vi.fn(async () => true),
    },
}));

vi.mock('$lib/storage', () => ({
    loadAppSetting: vi.fn(async () => 240),
    saveAppSetting: vi.fn(async () => {}),
    // The diff pane the panel preloads keeps its collapse state here.
    loadCollapsedDiffFiles: vi.fn(async () => new Set<string>()),
    saveCollapsedDiffFiles: vi.fn(async () => {}),
}));

import {
    openPanel, seedPanel, rowEl, rowEls, selectedPaths, bar, list, menu, STAGED, UNSTAGED,
} from './git-panel-harness';

beforeEach(() => {
    vi.clearAllMocks();
    seedPanel();
});

afterEach(() => cleanup());

// A finger's tap: the press the listing reads, then the click the browser sends once the finger is up.
async function tap(row: HTMLElement) {
    await fireEvent.pointerDown(row, { pointerType: 'touch', clientX: 40, clientY: 40 });
    await fireEvent.pointerUp(row, { pointerType: 'touch' });
    await fireEvent.click(row, { pointerType: 'touch' });
}

describe('the changes list selection', () => {
    it('selects the row it landed on and shows nothing else selected', async () => {
        const { container } = await openPanel();

        await fireEvent.click(rowEl(container, 'modified.ts')!);

        expect(selectedPaths(container)).toEqual(['modified.ts']);
        // One selected row is not a batch, so the action bar stays hidden.
        expect(bar(container)).toBeNull();
    });

    it('keeps the rows already held when ctrl-click adds another, across both sections', async () => {
        const { container } = await openPanel();

        await fireEvent.click(rowEl(container, 'staged-a.ts')!);
        await fireEvent.click(rowEl(container, 'scratch.ts')!, { ctrlKey: true });

        expect(selectedPaths(container)).toEqual(['staged-a.ts', 'scratch.ts']);
        expect(bar(container)?.querySelector('.selection-count')?.textContent).toBe('2 selected');
        expect([...bar(container)!.querySelectorAll('button')].map(b => b.getAttribute('aria-label')))
            .toEqual(['Stage', 'Unstage', 'Discard', 'Clear selection']);
    });

    it('takes the range between the anchor and the row a shift-click lands on', async () => {
        const { container } = await openPanel();

        await fireEvent.click(rowEl(container, 'staged-a.ts')!);
        await fireEvent.click(rowEl(container, 'staged-b.ts')!, { shiftKey: true });

        expect(selectedPaths(container)).toEqual([...STAGED]);
    });

    // The two sections are one list, so a range has to stop where its section does.
    it('keeps a shift range inside the section it started in', async () => {
        const { container } = await openPanel();

        await fireEvent.click(rowEl(container, 'staged-a.ts')!);
        await fireEvent.click(rowEl(container, 'scratch.ts')!, { shiftKey: true });

        expect(selectedPaths(container)).toEqual(['scratch.ts']);

        // The range then extends inside that section, whose own anchor was just set.
        await fireEvent.click(rowEl(container, 'modified.ts')!, { shiftKey: true });
        expect(selectedPaths(container)).toEqual([...UNSTAGED]);
    });

    it('selects a row held under a finger, without losing the row buttons', async () => {
        const { container } = await openPanel();
        const row = rowEl(container, 'modified.ts')!;

        await fireEvent.pointerDown(row, { pointerType: 'touch', clientX: 40, clientY: 40 });
        await new Promise(r => setTimeout(r, 550)); // LONG_PRESS_MS is 450

        expect(selectedPaths(container)).toEqual(['modified.ts']);
        expect(bar(container)?.querySelector('.selection-count')?.textContent).toBe('1 selected');

        // The long press ends in a click, which must not move the diff the panel shows.
        await fireEvent.click(row, { pointerType: 'touch' });
        expect(selectedPaths(container)).toEqual(['modified.ts']);
    });

    it('taps toggle rows while a finger holds a selection', async () => {
        const { container } = await openPanel();
        const held = rowEl(container, 'staged-a.ts')!;
        const row = rowEl(container, 'modified.ts')!;

        await fireEvent.pointerDown(held, { pointerType: 'touch', clientX: 40, clientY: 40 });
        await new Promise(r => setTimeout(r, 550)); // LONG_PRESS_MS is 450

        await tap(row);
        expect(selectedPaths(container)).toEqual(['staged-a.ts', 'modified.ts']);

        await tap(row);
        expect(selectedPaths(container)).toEqual(['staged-a.ts']);

        await tap(held);
        expect(selectedPaths(container)).toEqual([]);
        expect(bar(container)).toBeNull();
    });

    it('gives back a held row when the phone raises its context menu as well', async () => {
        const { container } = await openPanel();
        const held = rowEl(container, 'staged-a.ts')!;
        const row = rowEl(container, 'modified.ts')!;

        for (const r of [held, row]) {
            await fireEvent.pointerDown(r, { pointerType: 'touch', clientX: 40, clientY: 40 });
            await new Promise(r2 => setTimeout(r2, 550)); // LONG_PRESS_MS is 450
        }
        expect(selectedPaths(container)).toEqual(['staged-a.ts', 'modified.ts']);

        await fireEvent.pointerDown(row, { pointerType: 'touch', clientX: 40, clientY: 40 });
        await new Promise(r => setTimeout(r, 550));
        // A held finger raises contextmenu too; the hold already answered for that row.
        await fireEvent.contextMenu(row);

        expect(selectedPaths(container)).toEqual(['staged-a.ts']);
        expect(menu(container)).toBeNull();
    });

    it('selects every row on Ctrl+A and gives them all back on Escape', async () => {
        const onClose = vi.fn();
        const { container } = await openPanel({ onClose });

        await fireEvent.keyDown(list(container), { key: 'a', ctrlKey: true });
        expect(selectedPaths(container)).toEqual([...STAGED, ...UNSTAGED]);

        // Escape gives the rows back; the panel, which closes on the same key, stays open.
        await fireEvent.keyDown(list(container), { key: 'Escape' });
        expect(selectedPaths(container)).toEqual([]);
        expect(bar(container)).toBeNull();
        expect(onClose).not.toHaveBeenCalled();

        // With nothing held, the same key is the panel's own close.
        await fireEvent.keyDown(window, { key: 'Escape' });
        expect(onClose).toHaveBeenCalledTimes(1);
    });

    it('drops a row the reload removed from what is held', async () => {
        const { container } = await openPanel();

        await fireEvent.click(rowEl(container, 'modified.ts')!, { ctrlKey: true });
        await fireEvent.click(rowEl(container, 'scratch.ts')!, { ctrlKey: true });
        expect(selectedPaths(container)).toEqual([...UNSTAGED]);

        // A write the panel did not start committed both files, so the reload has no row for them.
        seedPanel('M  staged-a.ts\nA  staged-b.ts');
        (container.querySelector('.changes-section-action') as HTMLElement).click();
        await waitFor(() => expect(rowEls(container).length).toBe(2));
        expect(selectedPaths(container)).toEqual([]);
        expect(bar(container)).toBeNull();
    });
});
