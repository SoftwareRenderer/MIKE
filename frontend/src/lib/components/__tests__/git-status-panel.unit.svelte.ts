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
    fetchProjectRemote: vi.fn(),
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
const fetchProjectRemote = gitApi.fetchProjectRemote as ReturnType<typeof vi.fn>;
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
    fetchProjectRemote.mockResolvedValue({ ok: true, ahead: 0, behind: 0 });
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

    // The Commit button is never hidden — only its enablement and its reason move with the index.
    it.each([
        { staged: false, status: '', disabled: true, title: 'Nothing staged to commit' },
        { staged: true, status: 'M  staged.txt', disabled: false, title: 'Commit staged changes' },
    ])('keeps the Commit button on screen $staged, disabled $disabled', async ({ status, disabled, title }) => {
        if (status) {
            getGitStatus.mockResolvedValue({ clean: false, status, head: 'h1', authorName: 'Test', authorEmail: 'test@test.com', ahead: 0, behind: 0 });
        }

        const { container } = render(GitStatusPanel, { props: baseProps() });
        const btn = container.querySelector<HTMLButtonElement>('.panel-commit-btn');
        expect(btn).not.toBeNull();
        expect(btn!.textContent).toContain('Commit');

        // Present from the first render (not hidden), and unchanged once the status read has landed.
        await waitFor(() => expect(getGitStatus).toHaveBeenCalled());
        await waitFor(() => expect(btn!.disabled).toBe(disabled));
        expect(btn!.getAttribute('title')).toBe(title);
        await new Promise((resolve) => setTimeout(resolve, 10));
        expect(btn!.disabled).toBe(disabled);
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

    it('reloads the panel after a discard without asking the remote again', async () => {
        const { container } = render(GitStatusPanel, { props: baseProps() });
        await waitFor(() => expect(container.querySelector('.changes-file-row')).not.toBeNull());
        await waitFor(() => expect(fetchProjectRemote).toHaveBeenCalledTimes(1));

        discardBtn(container, 'staged.txt').click();

        await waitFor(() => expect(getGitStatus.mock.calls.length).toBeGreaterThan(1));
        expect(fetchProjectRemote).toHaveBeenCalledTimes(1);
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

    it('shows a dash until the remote has been asked, then the behind count on Pull', async () => {
        let answerFetch: (value: { ok: boolean; ahead: number; behind: number }) => void = () => {};
        fetchProjectRemote.mockImplementation(() => new Promise((resolve) => { answerFetch = resolve; }));

        const { container } = render(GitStatusPanel, { props: baseProps() });
        await waitFor(() => expect(fetchProjectRemote).toHaveBeenCalledWith('p1'));
        const pull = container.querySelector('button[aria-label^="Pull from remote"]')!;

        // The status read has landed and its number is withheld: it is the remote as of the last fetch.
        // The span is aria-hidden, so the number rides the accessible name.
        expect(counts(container).pull).toBe('—');
        expect(pull.getAttribute('aria-label')).toBe('Pull from remote (count not checked)');
        expect(pull.querySelector('.panel-count')?.getAttribute('aria-hidden')).toBe('true');

        answerFetch({ ok: true, ahead: 2, behind: 3 });

        await waitFor(() => expect(counts(container).pull).toBe('3'));
        expect(counts(container).push).toBe('2');
        expect(pull.getAttribute('aria-label')).toBe('Pull from remote (3 commits behind)');
    });

    it('asks the remote only once the opening status read has landed', async () => {
        // Without this order a slow status read repaints the number the fetch just corrected.
        let answerStatus: (value: unknown) => void = () => {};
        getGitStatus.mockImplementation(() => new Promise((resolve) => { answerStatus = resolve; }));
        let answerFetch: (value: { ok: boolean; ahead: number; behind: number }) => void = () => {};
        fetchProjectRemote.mockImplementation(() => new Promise((resolve) => { answerFetch = resolve; }));
        const { container } = render(GitStatusPanel, { props: baseProps() });
        await new Promise((resolve) => setTimeout(resolve, 0));

        expect(fetchProjectRemote).not.toHaveBeenCalled();

        answerStatus({ clean: true, status: '', head: 'h1', authorName: 'Test', authorEmail: 't@t.com', ahead: 0, behind: 7 });
        await waitFor(() => expect(fetchProjectRemote).toHaveBeenCalled());
        expect(counts(container).pull).toBe('—');

        answerFetch({ ok: true, ahead: 0, behind: 2 });
        await waitFor(() => expect(counts(container).pull).toBe('2'));
    });

    it('counts one commit in the singular on both buttons', async () => {
        fetchProjectRemote.mockResolvedValue({ ok: true, ahead: 1, behind: 1 });
        const { container } = render(GitStatusPanel, { props: baseProps() });

        const pull = container.querySelector('button[aria-label^="Pull from remote"]')!;
        const push = container.querySelector('button[aria-label^="Push to remote"]')!;
        await waitFor(() => expect(pull.getAttribute('aria-label')).toBe('Pull from remote (1 commit behind)'));
        expect(push.getAttribute('aria-label')).toBe('Push to remote (1 commit ahead)');
    });

    it('shows a dash rather than 0 when the remote could not be asked', async () => {
        // No upstream, an unreachable remote, a refused credential: the pair is the last snapshot, so no
        // number goes on the button.
        fetchProjectRemote.mockResolvedValue({ ok: false, ahead: 5, behind: 9 });
        const { container } = render(GitStatusPanel, { props: baseProps() });

        const pull = container.querySelector('button[aria-label^="Pull from remote"]')!;
        await waitFor(() => expect(counts(container).pull).toBe('—'));
        expect(pull.getAttribute('aria-label')).toBe('Pull from remote (count not checked)');
    });

    it('shows a dash when the fetch request itself fails', async () => {
        fetchProjectRemote.mockRejectedValue(new Error('offline'));
        const { container } = render(GitStatusPanel, { props: baseProps() });

        await waitFor(() => expect(fetchProjectRemote).toHaveBeenCalled());
        expect(counts(container).pull).toBe('—');
    });

    it('moves the behind count when the pull it reports lands', async () => {
        getGitStatus
            .mockResolvedValueOnce({ clean: true, status: '', head: 'h1', authorName: 'Test', authorEmail: 't@t.com', ahead: 0, behind: 4 })
            .mockResolvedValue({ clean: true, status: '', head: 'h1', authorName: 'Test', authorEmail: 't@t.com', ahead: 0, behind: 0 });
        fetchProjectRemote.mockResolvedValue({ ok: true, ahead: 0, behind: 4 });
        const { container } = render(GitStatusPanel, { props: baseProps() });
        await waitFor(() => expect(counts(container).pull).toBe('4'));

        container.querySelector<HTMLButtonElement>('button[aria-label^="Pull from remote"]')!.click();

        await waitFor(() => expect(pullProject).toHaveBeenCalledWith('p1'));
        await waitFor(() => expect(counts(container).pull).toBe('0'));
    });

    it('keeps the counts as they were when a push is rejected', async () => {
        getGitStatus.mockResolvedValue({ clean: true, status: '', head: 'h1', authorName: 'Test', authorEmail: 'test@test.com', ahead: 3, behind: 0 });
        fetchProjectRemote.mockResolvedValue({ ok: true, ahead: 3, behind: 0 });
        pushProject.mockRejectedValue(new Error('git push failed: rejected'));
        const { container } = render(GitStatusPanel, { props: baseProps() });
        await waitFor(() => expect(counts(container).push).toBe('3'));

        const push = container.querySelector('button[aria-label^="Push to remote"]') as HTMLButtonElement;
        push.click();
        await waitFor(() => expect(pushProject).toHaveBeenCalled());

        expect(counts(container).push).toBe('3');
        expect(push.getAttribute('aria-label')).toBe('Push to remote (3 commits ahead)');
    });
});

// Pull and Push disable each other, and a button's accessible name says only what it would do — so the
// one thing a reader cannot infer, *which* sync is holding the other, is the reason it owes them. A
// `title` cannot carry it: it needs a hover. The request is left unanswered, because the reason only
// exists while the panel stays busy.
describe('GitStatusPanel: a sync button waiting on the other one', () => {
    beforeEach(() => {
        pullProject.mockImplementation(() => new Promise(() => {}));
        pushProject.mockImplementation(() => new Promise(() => {}));
    });

    const pullBtn = (container: HTMLElement) =>
        container.querySelector('button[aria-label^="Pull from remote"]') as HTMLButtonElement;
    const pushBtn = (container: HTMLElement) =>
        container.querySelector('button[aria-label^="Push to remote"]') as HTMLButtonElement;

    /** The text behind `aria-describedby`, or null when the control describes nothing. */
    function describedBy(container: HTMLElement, el: Element): string | null {
        const id = el.getAttribute('aria-describedby');
        if (!id) return null;
        return container.querySelector(`#${id}`)?.textContent?.trim() ?? '';
    }

    it.each([
        { op: 'pull', reason: 'Pull in progress' },
        { op: 'push', reason: 'Push in progress' },
    ])('names the running $op to both buttons', async ({ op, reason }) => {
        const { container } = render(GitStatusPanel, { props: baseProps() });
        const running = op === 'pull' ? pullBtn(container) : pushBtn(container);
        const waiting = op === 'pull' ? pushBtn(container) : pullBtn(container);

        running.click();

        await waitFor(() => expect(running.hasAttribute('disabled')).toBe(true));
        expect(waiting.hasAttribute('disabled')).toBe(true);
        expect(describedBy(container, running)).toBe(reason);
        expect(describedBy(container, waiting)).toBe(reason);
    });

    it('describes nothing while neither is running', () => {
        const { container } = render(GitStatusPanel, { props: baseProps() });

        expect(pullBtn(container).hasAttribute('disabled')).toBe(false);
        expect(pushBtn(container).hasAttribute('disabled')).toBe(false);
        expect(describedBy(container, pullBtn(container))).toBeNull();
        expect(describedBy(container, pushBtn(container))).toBeNull();
    });
});

// The explorer's Changes panel is a second host of the same viewer. Which row wears a control at all is
// `binary-file-affordances.unit.svelte.ts`; these hold the panel's promise about the refs each side reads from.
describe('the binary row the explorer lists', () => {
    const IMAGE = { path: 'docs/logo.png', rows: [], added: 1, removed: 1, isBinary: true, fileSize: 67 };

    const imageButtons = (container: HTMLElement) =>
        [...container.querySelectorAll<HTMLButtonElement>('.diff-view-image-btn')];

    const pictureSources = (container: HTMLElement) =>
        [...container.querySelectorAll<HTMLImageElement>('img.image-diff-img')]
            .map((i) => new URL(i.getAttribute('src')!, 'http://x'));

    beforeEach(() => {
        getGitStatus.mockResolvedValue({
            clean: false, status: ' M docs/logo.png\n?? shots/new.png', head: 'h1',
            authorName: 'Test', authorEmail: 'test@test.com', ahead: 0, behind: 0,
        });
        getProjectDiffFiles.mockImplementation(async (_p: string, opts?: { cached?: boolean }) =>
            (opts?.cached ? [] : [{ ...IMAGE }]));
        getProjectDiffFile.mockImplementation(async (_p: string, _path: string, opts?: { meta?: boolean }) =>
            (opts?.meta ? { line_count: 1, isBinary: true, file_size: 67 } : { lines: [], line_count: 0 }));
    });

    it('offers the picture of an image the working tree changed, both sides of what it compares', async () => {
        const { container } = render(GitStatusPanel, { props: baseProps() });
        await waitFor(() => expect(imageButtons(container).length).toBeGreaterThan(0));

        const modified = imageButtons(container).find((b) => b.closest('.diff-file-section-untracked') === null)!;
        expect(modified).toBeDefined();
        modified.click();

        await waitFor(() => expect(pictureSources(container).length).toBe(2));
        const [oldSide, newSide] = pictureSources(container);
        expect(oldSide.pathname).toBe('/api/fs/read');
        expect(oldSide.searchParams.get('kind')).toBe('image');
        expect(oldSide.searchParams.get('project')).toBe('p1');
        expect(oldSide.searchParams.get('path')).toBe('docs/logo.png');
        expect(oldSide.searchParams.get('ref')).toBe('HEAD');
        expect(newSide.searchParams.has('ref')).toBe(false);
        // The panel compares the project's own checkout, so no task may steer the read.
        expect(oldSide.searchParams.has('task')).toBe(false);
    });

    it('offers one side for an image nobody has committed yet', async () => {
        const { container } = render(GitStatusPanel, { props: baseProps() });
        await waitFor(() => expect(imageButtons(container).length).toBe(2));

        const untracked = imageButtons(container).find((b) => b.closest('.diff-file-section-untracked') !== null)!;
        untracked.click();

        await waitFor(() => expect(pictureSources(container).length).toBe(1));
        const [only] = pictureSources(container);
        expect(only.searchParams.has('ref')).toBe(false);
        expect(only.searchParams.get('path')).toBe('shots/new.png');
    });

    // A clean tree puts the panel in its commit comparison, where From is the parent of the newest commit
    // and To is that commit — the two refs each side of the picture is read from.
    it('reads a commit comparison from the two commits the panel was shown', async () => {
        const head = { hash: '2'.repeat(40), short: '2222222', subject: 'Head', author: 'Test', date: '', parents: ['1'.repeat(40)] };
        const parent = { hash: '1'.repeat(40), short: '1111111', subject: 'Parent', author: 'Test', date: '', parents: [] };
        getGitStatus.mockResolvedValue({ clean: true, status: '', head: 'h1', authorName: 'Test', authorEmail: 'test@test.com', ahead: 0, behind: 0 });
        getGitLogFull.mockResolvedValue({ commits: [head, parent], current_branch: 'main' });
        getCommitDiffFiles.mockResolvedValue([{ ...IMAGE }]);

        const { container } = render(GitStatusPanel, { props: baseProps() });

        await waitFor(() => expect(getCommitDiffFiles).toHaveBeenCalledWith('p1', parent.hash, head.hash));
        await waitFor(() => expect(imageButtons(container).length).toBe(1));
        imageButtons(container)[0].click();

        await waitFor(() => expect(pictureSources(container).length).toBe(2));
        const [oldSide, newSide] = pictureSources(container);
        expect(oldSide.searchParams.get('ref')).toBe(parent.hash);
        expect(newSide.searchParams.get('ref')).toBe(head.hash);
    });
});
