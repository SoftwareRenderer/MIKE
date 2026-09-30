/**
 * What every changes-sidebar spec shares: one git status with two sections, the answers that let the panel
 * render rows, and the selectors its rows and menu answer to. The `vi.mock` blocks stay in each spec,
 * because vitest hoists them above the imports, so a factory cannot reach anything imported here.
 */
import { expect, vi } from 'vitest';
import { fireEvent, render, waitFor } from '@testing-library/svelte';
import GitStatusPanel from '$lib/components/GitStatusPanel.svelte';
import * as gitApi from '$lib/api/git';
import { dialogStore } from '$lib/stores';

const mock = (fn: unknown) => fn as ReturnType<typeof vi.fn>;

export const api = {
    getGitStatus: mock(gitApi.getGitStatus),
    getProjectDiffFiles: mock(gitApi.getProjectDiffFiles),
    getProjectDiffFile: mock(gitApi.getProjectDiffFile),
    stageProjectPaths: mock(gitApi.stageProjectPaths),
    discardProjectPaths: mock(gitApi.discardProjectPaths),
    getGitLogFull: mock(gitApi.getGitLogFull),
    getCommitDiffFiles: mock(gitApi.getCommitDiffFiles),
    showConfirm: mock(dialogStore.showConfirm),
    showToast: mock(dialogStore.showToast),
};

/** Two sections, the staged rows first. A file with both a staged and an unstaged change is listed once,
 *  as staged. */
export const STATUS = 'M  staged-a.ts\nA  staged-b.ts\n M modified.ts\n?? scratch.ts';
export const STAGED = ['staged-a.ts', 'staged-b.ts'];
export const UNSTAGED = ['modified.ts', 'scratch.ts'];

const COMMIT = {
    hash: 'a'.repeat(40), short: 'aaaaaaa', subject: 'Initial commit',
    author: 'Test', date: '2026-01-01T00:00:00Z',
};

/** The answers that make the panel list `status`. Call after `vi.clearAllMocks()`. */
export function seedPanel(status = STATUS): void {
    api.getGitStatus.mockResolvedValue({
        clean: status === '', status, head: 'h1', authorName: 'Test', authorEmail: 't@t.com',
        ahead: 0, behind: 0,
    });
    api.getProjectDiffFiles.mockResolvedValue([]);
    api.getProjectDiffFile.mockRejectedValue(new Error('no untracked files'));
    api.getGitLogFull.mockResolvedValue({ commits: [COMMIT], current_branch: 'main' });
    api.getCommitDiffFiles.mockResolvedValue([]);
    // The endpoints answer with the paths they landed on; every seeded path is answered as applied.
    api.stageProjectPaths.mockImplementation(async (_p: string, paths: string[], stage: boolean) =>
        ({ status: 'ok', action: stage ? 'staged' : 'unstaged', applied: [...paths], failed: [] }));
    api.discardProjectPaths.mockImplementation(async (_p: string, paths: string[]) =>
        ({ status: 'ok', action: 'discarded', applied: [...paths], failed: [] }));
    api.showConfirm.mockResolvedValue(true);
}

const baseProps = () => ({ projectId: 'p1', projectPath: '/tmp/p1', onClose: vi.fn(), isMobile: false });
export const rowEls = (container: HTMLElement) =>
    [...container.querySelectorAll<HTMLElement>('.changes-file-item')];
export const rowEl = (container: HTMLElement, path: string) =>
    container.querySelector<HTMLElement>(`.changes-file-item[title="${path}"]`);
/** The selected rows, read from the rows rather than from the component. */
export const selectedPaths = (container: HTMLElement) =>
    rowEls(container).filter(r => r.getAttribute('aria-selected') === 'true').map(r => r.title);
export const bar = (container: HTMLElement) => container.querySelector<HTMLElement>('.file-selection-bar');
export const list = (container: HTMLElement) => container.querySelector<HTMLElement>('.changes-scroll')!;
/** A row's own write buttons: only the one that was pressed shows a spinner. */
export const stageBtn = (container: HTMLElement, path: string) =>
    rowEl(container, path)?.parentElement?.querySelector<HTMLElement>('.changes-stage-btn') ?? null;
export const discardRowBtn = (container: HTMLElement, path: string) =>
    rowEl(container, path)?.parentElement?.querySelector<HTMLElement>('.changes-discard-btn') ?? null;
/** A group header's whole-section action, named by what it does. */
export const sectionAction = (container: HTMLElement, label: string) =>
    container.querySelector<HTMLElement>(`.changes-section-action[aria-label="${label}"]`);
/** Every spinner a running write leaves behind, wherever it sits. */
export const rings = (container: HTMLElement) => [...container.querySelectorAll('.spinner')];
export const menu = (container: HTMLElement) => container.querySelector<HTMLElement>('.file-context-menu');
export const menuLabels = (container: HTMLElement) =>
    [...menu(container)!.querySelectorAll('.menu-label')].map(s => s.textContent);

export function menuItem(container: HTMLElement, label: string): HTMLButtonElement {
    const button = [...menu(container)!.querySelectorAll<HTMLButtonElement>('button')]
        .find(b => b.querySelector('.menu-label')?.textContent === label);
    if (!button) throw new Error(`no menu item "${label}" in [${menuLabels(container)}]`);
    return button;
}

export async function openPanel(props: Record<string, unknown> = {}) {
    const rendered = render(GitStatusPanel, { props: { ...baseProps(), ...props } });
    await waitFor(() => expect(rowEls(rendered.container).length).toBe(4));
    return rendered;
}

/** The rows a batch is asked for: the working-tree ones, held without opening anything else. */
export async function holdUnstaged(container: HTMLElement) {
    await fireEvent.click(rowEl(container, 'modified.ts')!);
    await fireEvent.click(rowEl(container, 'scratch.ts')!, { ctrlKey: true });
}

export async function openMenuOn(container: HTMLElement, path: string) {
    await fireEvent.contextMenu(rowEl(container, path)!, { clientX: 40, clientY: 60 });
    await waitFor(() => expect(menu(container)).not.toBeNull());
}
