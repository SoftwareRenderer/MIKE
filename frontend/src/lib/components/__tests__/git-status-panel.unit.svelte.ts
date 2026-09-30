import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, cleanup, waitFor } from '@testing-library/svelte';

// Run onMount callbacks in a microtask (after first render) so the panel's
// async data loading can update state. The global setup no-ops onMount.
vi.mock('svelte', async (importOriginal) => {
    const actual = await importOriginal<typeof import('svelte')>();
    return {
        ...actual,
        onMount: (cb: () => void | Promise<void>) => {
            void Promise.resolve().then(() => cb());
        },
    };
});

// Mock the git API — every function the panel touches.
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

import GitStatusPanel from '$lib/components/GitStatusPanel.svelte';
import * as gitApi from '$lib/api/git';
import { dialogStore } from '$lib/stores';

const getGitStatus = gitApi.getGitStatus as ReturnType<typeof vi.fn>;
const getProjectDiffFiles = gitApi.getProjectDiffFiles as ReturnType<typeof vi.fn>;
const getProjectDiffFile = gitApi.getProjectDiffFile as ReturnType<typeof vi.fn>;
const pullProject = gitApi.pullProject as ReturnType<typeof vi.fn>;
const pushProject = gitApi.pushProject as ReturnType<typeof vi.fn>;
const discardProjectPaths = gitApi.discardProjectPaths as ReturnType<typeof vi.fn>;
const getGitLogFull = gitApi.getGitLogFull as ReturnType<typeof vi.fn>;
const getCommitDiffFiles = gitApi.getCommitDiffFiles as ReturnType<typeof vi.fn>;

const COMMIT = {
    hash: 'a'.repeat(40),
    short: 'aaaaaaa',
    subject: 'Initial commit',
    author: 'Test',
    date: '2026-01-01T00:00:00Z',
};

function baseProps() {
    return {
        projectId: 'p1',
        projectPath: '/tmp/p1',
        onClose: vi.fn(),
        isMobile: false,
    };
}

beforeEach(() => {
    vi.clearAllMocks();
    // Clean working tree by default.
    getGitStatus.mockResolvedValue({ clean: true, status: '', head: 'h1', authorName: 'Test', authorEmail: 'test@test.com', ahead: 0, behind: 0 });
    getProjectDiffFiles.mockResolvedValue([]);
    getProjectDiffFile.mockRejectedValue(new Error('no untracked files'));
    getGitLogFull.mockResolvedValue({ commits: [COMMIT], current_branch: 'main' });
    getCommitDiffFiles.mockResolvedValue([]);
    pullProject.mockResolvedValue({ status: 'pulled' });
    pushProject.mockResolvedValue({ status: 'pushed' });
    discardProjectPaths.mockImplementation(async (_p: string, paths: string[]) =>
        ({ status: 'ok', action: 'discarded', applied: [...paths], failed: [] }));
});

afterEach(() => {
    cleanup();
});

describe('GitStatusPanel header actions', () => {
    it('loads staged and unstaged diffs through the server-parsed API', async () => {
        // Guards the diff-grammar move. If the panel calls a function the module no longer exports, the call
        // throws inside Promise.all and .catch(() => []) turns it into an empty diff - so every other test here
        // would still pass on a broken panel. Asserting the call is what makes that visible.
        render(GitStatusPanel, { props: baseProps() });
        await waitFor(() => expect(getProjectDiffFiles).toHaveBeenCalledWith('p1', { cached: true }));
        expect(getProjectDiffFiles).toHaveBeenCalledWith('p1');
    });

    it('always renders the Commit button, disabled when nothing is staged', async () => {
        const { container } = render(GitStatusPanel, { props: baseProps() });

        // The button is present from the first render (not hidden).
        const btn = container.querySelector<HTMLButtonElement>('.panel-commit-btn');
        expect(btn).not.toBeNull();
        expect(btn!.disabled).toBe(true);
        expect(btn!.getAttribute('title')).toBe('Nothing staged to commit');
        expect(btn!.textContent).toContain('Commit');

        // After the (clean) load completes, it stays disabled.
        await waitFor(() => expect(container.querySelector('.empty-state')).not.toBeNull());
        expect(btn!.disabled).toBe(true);
    });

    it('enables the Commit button when there are staged entries', async () => {
        getGitStatus.mockResolvedValue({ clean: false, status: 'M  staged.txt', head: 'h1', authorName: 'Test', authorEmail: 'test@test.com', ahead: 0, behind: 0 });

        const { container } = render(GitStatusPanel, { props: baseProps() });
        const btn = container.querySelector<HTMLButtonElement>('.panel-commit-btn');
        expect(btn).not.toBeNull();

        await waitFor(() => expect(btn!.disabled).toBe(false));
        expect(btn!.getAttribute('title')).toBe('Commit staged changes');
    });

    it('renders Pull and Push buttons with tooltips', () => {
        const { container } = render(GitStatusPanel, { props: baseProps() });

        const pull = container.querySelector('button[aria-label^="Pull from remote"]');
        const push = container.querySelector('button[aria-label^="Push to remote"]');
        expect(pull).not.toBeNull();
        expect(push).not.toBeNull();

        // The in-panel Refresh button is gone — rely on the browser's reload.
        expect(container.querySelector('button[aria-label="Refresh"]')).toBeNull();
    });

    it('pull button calls the pull API, shows a success toast, and fires onUpdated', async () => {
        const onUpdated = vi.fn();
        const { container } = render(GitStatusPanel, { props: { ...baseProps(), onUpdated } });
        const pull = container.querySelector('button[aria-label^="Pull from remote"]') as HTMLButtonElement;

        pull.click();

        await waitFor(() => expect(pullProject).toHaveBeenCalledWith('p1'));
        await waitFor(() =>
            expect(dialogStore.showToast).toHaveBeenCalledWith('Pulled from remote', 'success')
        );
        await waitFor(() => expect(onUpdated).toHaveBeenCalledTimes(1));
    });

    it('push button calls the push API, shows a success toast, and fires onUpdated', async () => {
        const onUpdated = vi.fn();
        const { container } = render(GitStatusPanel, { props: { ...baseProps(), onUpdated } });
        const push = container.querySelector('button[aria-label^="Push to remote"]') as HTMLButtonElement;

        push.click();

        await waitFor(() => expect(pushProject).toHaveBeenCalledWith('p1'));
        await waitFor(() =>
            expect(dialogStore.showToast).toHaveBeenCalledWith('Pushed to remote', 'success')
        );
        await waitFor(() => expect(onUpdated).toHaveBeenCalledTimes(1));
    });

    it('shows an error toast with the API message when push fails', async () => {
        const onUpdated = vi.fn();
        pushProject.mockRejectedValue(new Error('git push failed: fatal: The current branch main has no upstream branch.'));

        const { container } = render(GitStatusPanel, { props: { ...baseProps(), onUpdated } });
        const push = container.querySelector('button[aria-label^="Push to remote"]') as HTMLButtonElement;

        push.click();

        await waitFor(() =>
            expect(dialogStore.showToast).toHaveBeenCalledWith(
                'git push failed: fatal: The current branch main has no upstream branch.',
                'error'
            )
        );
        expect(onUpdated).not.toHaveBeenCalled();
    });
});

describe('GitStatusPanel discard', () => {
    const STAGED_AND_UNSTAGED = 'M  staged.txt\nA  brand-new.txt\n M modified.txt\n?? scratch.txt';

    beforeEach(() => {
        getGitStatus.mockResolvedValue({
            clean: false, status: STAGED_AND_UNSTAGED, head: 'h1', authorName: 'Test', authorEmail: 'test@test.com',
        });
    });

    function discardBtn(container: HTMLElement, path: string): HTMLButtonElement {
        const btn = container.querySelector<HTMLButtonElement>(`button[aria-label="Discard ${path}"]`);
        if (!btn) throw new Error(`no discard button for ${path}`);
        return btn;
    }

    it('offers discard on every listed row', async () => {
        const { container } = render(GitStatusPanel, { props: baseProps() });

        await waitFor(() => expect(container.querySelector('.changes-file-row')).not.toBeNull());
        for (const path of ['staged.txt', 'brand-new.txt', 'modified.txt', 'scratch.txt']) {
            expect(container.querySelector(`button[aria-label="Discard ${path}"]`)).not.toBeNull();
        }
    });

    // A discard has no undo, so a declined dialog must send nothing at all.
    it('sends nothing when the confirmation is declined', async () => {
        (dialogStore.showConfirm as ReturnType<typeof vi.fn>).mockResolvedValueOnce(false);
        const { container } = render(GitStatusPanel, { props: baseProps() });
        await waitFor(() => expect(container.querySelector('.changes-file-row')).not.toBeNull());

        discardBtn(container, 'modified.txt').click();
        await waitFor(() => expect(dialogStore.showConfirm).toHaveBeenCalled());
        // A macrotask: the request is queued behind the dialog's promise, so the next microtask would
        // pass even if the panel sent it.
        await new Promise((resolve) => setTimeout(resolve, 0));

        expect(discardProjectPaths).not.toHaveBeenCalled();
        expect(dialogStore.showToast).not.toHaveBeenCalled();
    });

    // The wording each letter must produce. `A` and `?` name a path no commit holds, so those are
    // destroyed; `M` goes back to HEAD.
    const DISCARD_COPY: [string, string][] = [
        ['staged.txt', 'goes back to its last commit'],
        ['modified.txt', 'goes back to its last commit'],
        ['brand-new.txt', 'deleted from disk'],
        ['scratch.txt', 'deleted from disk'],
    ];

    it('names the file and says which shape of loss is coming', async () => {
        const { container } = render(GitStatusPanel, { props: baseProps() });
        await waitFor(() => expect(container.querySelector('.changes-file-row')).not.toBeNull());

        let dialogs = 0;
        for (const [path, words] of DISCARD_COPY) {
            // Decline: an accepted dialog reloads the panel and replaces the rows this loop reads next.
            (dialogStore.showConfirm as ReturnType<typeof vi.fn>).mockResolvedValueOnce(false);
            discardBtn(container, path).click();
            dialogs++;
            await waitFor(() => expect(vi.mocked(dialogStore.showConfirm).mock.calls).toHaveLength(dialogs));
            expect(vi.mocked(dialogStore.showConfirm).mock.calls[dialogs - 1][0]).toMatchObject({
                title: 'Discard changes?',
                message: expect.stringContaining(words),
                items: [path],
                variant: 'destructive',
            });
        }
    });

    it('discards the confirmed file and reloads the panel', async () => {
        const { container } = render(GitStatusPanel, { props: baseProps() });
        await waitFor(() => expect(container.querySelector('.changes-file-row')).not.toBeNull());
        const loadsBefore = getGitStatus.mock.calls.length;

        discardBtn(container, 'staged.txt').click();

        await waitFor(() => expect(discardProjectPaths).toHaveBeenCalledWith('p1', ['staged.txt']));
        expect(discardProjectPaths).toHaveBeenCalledTimes(1);
        await waitFor(() => expect(dialogStore.showToast).toHaveBeenCalledWith('Changes discarded', 'success'));
        await waitFor(() => expect(getGitStatus.mock.calls.length).toBeGreaterThan(loadsBefore));
    });

    it('reports a failed discard and leaves the diff pane alone', async () => {
        discardProjectPaths.mockRejectedValue(new Error('git discard failed: index.lock exists'));
        const { container } = render(GitStatusPanel, { props: baseProps() });
        await waitFor(() => expect(container.querySelector('.changes-file-row')).not.toBeNull());
        const loadsBefore = getGitStatus.mock.calls.length;

        discardBtn(container, 'modified.txt').click();

        await waitFor(() =>
            expect(dialogStore.showToast).toHaveBeenCalledWith('git discard failed: index.lock exists', 'error')
        );
        expect(getGitStatus.mock.calls.length).toBe(loadsBefore);
    });
});

describe('GitStatusPanel pull/push commit counts', () => {
    function counts(container: HTMLElement) {
        const spans = container.querySelectorAll('.panel-icon-btn .panel-count');
        return { pull: spans[0]?.textContent, push: spans[1]?.textContent };
    }

    it('puts the behind count on Pull and the ahead count on Push, labelled approximate before this panel has synced', async () => {
        getGitStatus.mockResolvedValue({ clean: true, status: '', head: 'h1', authorName: 'Test', authorEmail: 'test@test.com', ahead: 2, behind: 3 });

        const { container } = render(GitStatusPanel, { props: baseProps() });
        await waitFor(() => expect(counts(container).pull).toBe('3'));
        expect(counts(container).push).toBe('2');

        // The visible span is aria-hidden, so the numbers have to ride the accessible name.
        const pull = container.querySelector('button[aria-label^="Pull from remote"]')!;
        expect(pull.getAttribute('aria-label')).toBe('Pull from remote (3 commits behind) — as of the last pull or push');
        expect(pull.querySelector('.panel-count')?.getAttribute('aria-hidden')).toBe('true');
    });

    it('shows the singular and drops the sync note once the panel has synced', async () => {
        getGitStatus.mockResolvedValue({ clean: true, status: '', head: 'h1', authorName: 'Test', authorEmail: 'test@test.com', ahead: 1, behind: 0 });
        const { container } = render(GitStatusPanel, { props: baseProps() });
        const push = container.querySelector('button[aria-label^="Push to remote"]') as HTMLButtonElement;

        push.click();
        await waitFor(() => expect(pushProject).toHaveBeenCalled());

        await waitFor(() => expect(counts(container).push).toBe('1'));
        expect(push.getAttribute('aria-label')).toBe('Push to remote (1 commit ahead)');
    });

    it('renders 0 for a branch that is in sync or has no upstream', async () => {
        const { container } = render(GitStatusPanel, { props: baseProps() });

        await waitFor(() => expect(counts(container).pull).toBe('0'));
        expect(counts(container).push).toBe('0');
    });

    it('labels the counts exact after a pull, which fetched', async () => {
        getGitStatus.mockResolvedValue({ clean: true, status: '', head: 'h1', authorName: 'Test', authorEmail: 'test@test.com', ahead: 0, behind: 4 });
        const { container } = render(GitStatusPanel, { props: baseProps() });
        const pull = container.querySelector('button[aria-label^="Pull from remote"]') as HTMLButtonElement;
        await waitFor(() => expect(counts(container).pull).toBe('4'));

        pull.click();

        await waitFor(() => expect(pull.getAttribute('aria-label')).toBe('Pull from remote (4 commits behind)'));
        expect(counts(container).pull).toBe('4');
        expect(counts(container).push).toBe('0');
    });

    it('keeps the counts labelled approximate when the remote call fails', async () => {
        getGitStatus.mockResolvedValue({ clean: true, status: '', head: 'h1', authorName: 'Test', authorEmail: 'test@test.com', ahead: 3, behind: 0 });
        pushProject.mockRejectedValue(new Error('git push failed: rejected'));
        const { container } = render(GitStatusPanel, { props: baseProps() });
        await waitFor(() => expect(counts(container).push).toBe('3'));

        const push = container.querySelector('button[aria-label^="Push to remote"]') as HTMLButtonElement;
        push.click();
        await waitFor(() => expect(pushProject).toHaveBeenCalled());

        expect(counts(container).push).toBe('3');
        expect(push.getAttribute('aria-label')).toBe('Push to remote (3 commits ahead) — as of the last pull or push');
    });
});
