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
    stageProjectFile: vi.fn(),
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
}));

import GitStatusPanel from '$lib/components/GitStatusPanel.svelte';
import * as gitApi from '$lib/api/git';
import { dialogStore } from '$lib/stores';

const getGitStatus = gitApi.getGitStatus as ReturnType<typeof vi.fn>;
const getProjectDiffFiles = gitApi.getProjectDiffFiles as ReturnType<typeof vi.fn>;
const getProjectDiffFile = gitApi.getProjectDiffFile as ReturnType<typeof vi.fn>;
const pullProject = gitApi.pullProject as ReturnType<typeof vi.fn>;
const pushProject = gitApi.pushProject as ReturnType<typeof vi.fn>;
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
    getGitStatus.mockResolvedValue({ clean: true, status: '', head: 'h1', authorName: 'Test', authorEmail: 'test@test.com' });
    getProjectDiffFiles.mockResolvedValue([]);
    getProjectDiffFile.mockRejectedValue(new Error('no untracked files'));
    getGitLogFull.mockResolvedValue({ commits: [COMMIT], current_branch: 'main' });
    getCommitDiffFiles.mockResolvedValue([]);
    pullProject.mockResolvedValue({ status: 'pulled' });
    pushProject.mockResolvedValue({ status: 'pushed' });
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
        await waitFor(() => expect(container.querySelector('.panel-empty')).not.toBeNull());
        expect(btn!.disabled).toBe(true);
    });

    it('enables the Commit button when there are staged entries', async () => {
        getGitStatus.mockResolvedValue({ clean: false, status: 'M  staged.txt', head: 'h1', authorName: 'Test', authorEmail: 'test@test.com' });

        const { container } = render(GitStatusPanel, { props: baseProps() });
        const btn = container.querySelector<HTMLButtonElement>('.panel-commit-btn');
        expect(btn).not.toBeNull();

        await waitFor(() => expect(btn!.disabled).toBe(false));
        expect(btn!.getAttribute('title')).toBe('Commit staged changes');
    });

    it('renders Pull and Push buttons with tooltips', () => {
        const { container } = render(GitStatusPanel, { props: baseProps() });

        const pull = container.querySelector('button[aria-label="Pull from remote"]');
        const push = container.querySelector('button[aria-label="Push to remote"]');
        expect(pull).not.toBeNull();
        expect(push).not.toBeNull();
        expect(pull!.getAttribute('title')).toBe('Pull from remote');
        expect(push!.getAttribute('title')).toBe('Push to remote');

        // The in-panel Refresh button is gone — rely on the browser's reload.
        expect(container.querySelector('button[aria-label="Refresh"]')).toBeNull();
    });

    it('pull button calls the pull API, shows a success toast, and fires onUpdated', async () => {
        const onUpdated = vi.fn();
        const { container } = render(GitStatusPanel, { props: { ...baseProps(), onUpdated } });
        const pull = container.querySelector('button[aria-label="Pull from remote"]') as HTMLButtonElement;

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
        const push = container.querySelector('button[aria-label="Push to remote"]') as HTMLButtonElement;

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
        const push = container.querySelector('button[aria-label="Push to remote"]') as HTMLButtonElement;

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
