// @vitest-environment jsdom
// An unanswered git write is a wait the panel has to show: the control that was pressed carries the spinner,
// and every other write control is dead until the answer arrives.
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
    api, openPanel, seedPanel, rowEl, stageBtn, discardRowBtn, sectionAction, rings,
} from './git-panel-harness';

/** A write the test decides when to answer. */
function heldWrite() {
    let resolve: (value: { status: string; action: string; applied: string[]; failed: { path: string; error: string }[] }) => void = () => {};
    const promise = new Promise<unknown>((res) => { resolve = res; });
    api.stageProjectPaths.mockImplementation(() => promise);
    api.discardProjectPaths.mockImplementation(() => promise);
    return {
        answered: () => {
            resolve({ status: 'ok', action: 'staged', applied: [], failed: [] });
            return promise;
        },
    };
}

beforeEach(() => {
    vi.clearAllMocks();
    seedPanel();
});

afterEach(() => cleanup());

describe('a stage write in flight', () => {
    it('rings the row button that was pressed and nothing else', async () => {
        const { container } = await openPanel();
        const held = heldWrite();

        fireEvent.click(stageBtn(container, 'modified.ts')!);
        await waitFor(() => expect(api.stageProjectPaths).toHaveBeenCalled());

        expect(stageBtn(container, 'modified.ts')!.querySelector('.spinner')).not.toBeNull();
        expect(stageBtn(container, 'modified.ts')!.getAttribute('aria-busy')).toBe('true');
        expect(stageBtn(container, 'scratch.ts')!.querySelector('.spinner')).toBeNull();
        expect(sectionAction(container, 'Stage all')!.querySelector('.spinner')).toBeNull();

        held.answered();
        await waitFor(() => expect(stageBtn(container, 'modified.ts')!.querySelector('.spinner')).toBeNull());
    });

    it('names the wait in words, not only in a ring', async () => {
        const { container } = await openPanel();
        const held = heldWrite();

        fireEvent.click(stageBtn(container, 'modified.ts')!);
        await waitFor(() => expect(rings(container).length).toBeGreaterThan(0));

        const announced = container.querySelector('.sr-only#git-write-busy');
        expect(announced?.textContent).toBe('Staging in progress');
        expect(stageBtn(container, 'modified.ts')!.getAttribute('aria-describedby')).toBe('git-write-busy');

        held.answered();
        await waitFor(() => expect(container.querySelector('.sr-only#git-write-busy')).toBeNull());
    });

    it('kills every other write control until the answer lands', async () => {
        const { container } = await openPanel();
        const held = heldWrite();

        fireEvent.click(stageBtn(container, 'modified.ts')!);
        await waitFor(() => expect(api.stageProjectPaths).toHaveBeenCalled());

        expect(stageBtn(container, 'scratch.ts')!.hasAttribute('disabled')).toBe(true);
        expect(discardRowBtn(container, 'staged-a.ts')!.hasAttribute('disabled')).toBe(true);

        // A second click cannot slip into the queue behind the running write.
        fireEvent.click(stageBtn(container, 'scratch.ts')!);
        await new Promise(resolve => setTimeout(resolve, 0));
        expect(api.stageProjectPaths).toHaveBeenCalledTimes(1);

        held.answered();
        await waitFor(() => expect(stageBtn(container, 'scratch.ts')!.hasAttribute('disabled')).toBe(false));
    });

    it('rings the section action that asked for the whole section', async () => {
        const { container } = await openPanel();
        const held = heldWrite();

        fireEvent.click(sectionAction(container, 'Stage all')!);
        await waitFor(() => expect(api.stageProjectPaths).toHaveBeenCalledWith('p1', ['modified.ts', 'scratch.ts'], true));

        expect(sectionAction(container, 'Stage all')!.querySelector('.spinner')).not.toBeNull();
        expect(sectionAction(container, 'Unstage all')!.querySelector('.spinner')).toBeNull();

        held.answered();
        await waitFor(() => expect(sectionAction(container, 'Stage all')!.querySelector('.spinner')).toBeNull());
    });
});

describe('a discard write in flight', () => {
    it('rings the row discard button, and says so in words', async () => {
        const { container } = await openPanel();
        let resolve: (value: unknown) => void = () => {};
        api.discardProjectPaths.mockImplementation(() => new Promise(res => { resolve = res; }));

        fireEvent.click(discardRowBtn(container, 'scratch.ts')!);
        await waitFor(() => expect(api.discardProjectPaths).toHaveBeenCalledWith('p1', ['scratch.ts']));

        expect(discardRowBtn(container, 'scratch.ts')!.querySelector('.spinner')).not.toBeNull();
        expect(stageBtn(container, 'scratch.ts')!.querySelector('.spinner')).toBeNull();
        expect(container.querySelector('.sr-only#git-write-busy')?.textContent).toBe('Discarding in progress');

        resolve({ status: 'ok', action: 'discarded', applied: ['scratch.ts'], failed: [] });
        await waitFor(() => expect(discardRowBtn(container, 'scratch.ts')!.querySelector('.spinner')).toBeNull());
    });
});
