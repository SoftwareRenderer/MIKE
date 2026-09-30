// @vitest-environment jsdom
// The task diff panel shows the same wait: a file whose write has not been answered spins in place of its ±
// sign and cannot be clicked again, and a whole-section action spins only in the header it was pressed in.
import { describe, it, expect, afterEach, vi } from 'vitest';
import { render, cleanup, fireEvent } from '@testing-library/svelte';
import DiffViewer from '$lib/components/DiffViewer.svelte';
import type { DiffFile } from '$lib/types/diff';

vi.mock('svelte/transition', () => ({ slide: vi.fn(), fade: vi.fn() }));

afterEach(cleanup);

function file(path: string, staged = false): DiffFile {
    return { path, rows: [{ lineNumber: 1, variant: 'added', text: 'x' }], added: 1, removed: 0, staged };
}

function stageBtnFor(container: HTMLElement, path: string): HTMLElement | null {
    const header = [...container.querySelectorAll<HTMLElement>('.diff-file-path')]
        .find(el => el.textContent?.includes(path))
        ?.closest('.diff-file-header-row');
    return header?.querySelector<HTMLElement>('.diff-stage-btn') ?? null;
}

function sectionActionFor(container: HTMLElement, label: string): HTMLElement | null {
    return container.querySelector<HTMLElement>(`.diff-section-action[aria-label="${label}"]`);
}

function open(props: Record<string, unknown> = {}) {
    return render(DiffViewer, {
        props: {
            stagedFiles: [file('kept.ts', true)],
            unstagedFiles: [file('pending.ts'), file('other.ts')],
            stagedUntracked: [],
            unstagedUntracked: [],
            onStageFile: vi.fn(),
            onStageAll: vi.fn(),
            onUnstageAll: vi.fn(),
            ...props,
        },
    });
}

describe('a stage write in flight in the diff panel', () => {
    it('replaces the ± sign with a ring on the file being written, and only that file', () => {
        const { container } = open({ pendingPaths: new Set(['pending.ts']) });

        expect(stageBtnFor(container, 'pending.ts')!.querySelector('.spinner')).not.toBeNull();
        expect(stageBtnFor(container, 'pending.ts')!.hasAttribute('disabled')).toBe(true);
        expect(stageBtnFor(container, 'pending.ts')!.getAttribute('aria-busy')).toBe('true');
        expect(stageBtnFor(container, 'other.ts')!.querySelector('.spinner')).toBeNull();
        expect(stageBtnFor(container, 'other.ts')!.hasAttribute('disabled')).toBe(false);
    });

    it('takes the file out of reach while its write is out', () => {
        const onStageFile = vi.fn();
        const { container } = open({ pendingPaths: new Set(['pending.ts']), onStageFile });

        const button = stageBtnFor(container, 'pending.ts')!;
        // A dead button, so neither the click nor the key reaches git.
        expect(button.hasAttribute('disabled')).toBe(true);
        expect(button.getAttribute('tabindex')).toBeNull();
    });

    it('rings the whole-section action that is running, and stays inert while it does', () => {
        const onStageAll = vi.fn();
        const { container } = open({ busySection: 'unstaged', busyLabel: 'Staging changes...', onStageAll });

        const action = sectionActionFor(container, 'Stage all changes')!;
        expect(action.querySelector('.spinner')).not.toBeNull();
        expect(sectionActionFor(container, 'Unstage all changes')!.querySelector('.spinner')).toBeNull();

        fireEvent.click(action);
        expect(onStageAll).not.toHaveBeenCalled();
    });

    it('will not start a whole-section action while a single file is still being written', () => {
        const onStageAll = vi.fn();
        const onUnstageAll = vi.fn();
        const { container } = open({ pendingPaths: new Set(['pending.ts']), onStageAll, onUnstageAll });

        fireEvent.click(sectionActionFor(container, 'Stage all changes')!);
        fireEvent.click(sectionActionFor(container, 'Unstage all changes')!);

        expect(onStageAll).not.toHaveBeenCalled();
        expect(onUnstageAll).not.toHaveBeenCalled();
    });

    it('names the wait once for the controls the ring replaced', () => {
        const { container } = open({ pendingPaths: new Set(['pending.ts']), busyLabel: 'Staging changes...' });

        expect(container.querySelector('#diff-stage-busy')?.textContent).toBe('Staging changes...');
        expect(stageBtnFor(container, 'pending.ts')!.getAttribute('aria-describedby')).toBe('diff-stage-busy');
    });

    it('shows no ring at all when nothing is in flight', () => {
        const { container } = open();

        expect(container.querySelectorAll('.spinner').length).toBe(0);
        expect(container.querySelector('#diff-stage-busy')).toBeNull();
    });
});
