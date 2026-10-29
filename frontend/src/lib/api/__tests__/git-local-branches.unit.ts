import { describe, it, expect, vi, beforeEach } from 'vitest';

/**
 * Which refs a branch control may put in front of a person, measured at the one layer that decides it.
 * `fetch` is stubbed, so no request leaves the process.
 */

let mockFetch: ReturnType<typeof vi.fn>;

beforeEach(() => {
    mockFetch = vi.fn();
    (globalThis as any).fetch = mockFetch;
});

function res(status: number, body: any) {
    return {
        ok: status >= 200 && status < 300,
        status,
        headers: new Headers(),
        json: async () => body,
    };
}

/** The route's rows, shaped the way the route shapes them: `worktree` is `omitempty`, so a ref this checkout
 * may take carries no such key. */
const ROWS = [
    { name: 'main' },
    { name: 'origin/main' },
    { name: 'feature/x' },
    { name: 'task/fix-the-thing-abc123', worktree: '/work/.worktrees/MIKE/amber-hall', task_id: 'amber-hall' },
    { name: 'task/unrowed-def456', worktree: '/home/operator/other-checkout' },
    { name: 'HEAD' },
    { name: 'origin/HEAD' },
    { name: 'zeta' },
];

describe('getLocalBranches', () => {
    it('drops remote-tracking refs and HEAD and keeps the server order', async () => {
        const { getLocalBranches } = await import('$lib/api/git');
        mockFetch.mockResolvedValue(res(200, ROWS));

        const branches = await getLocalBranches('p1');

        // A ref another worktree holds stays: naming a target branch is not switching to it.
        expect(branches).toEqual([
            'main', 'feature/x', 'task/fix-the-thing-abc123', 'task/unrowed-def456', 'zeta',
        ]);
    });

    it('asks the branches route of the project it was given', async () => {
        const { getLocalBranches } = await import('$lib/api/git');
        mockFetch.mockResolvedValue(res(200, []));

        await getLocalBranches('p1');

        // Importing the api layer may ask for a session first, so look for this request among the calls.
        const urls = mockFetch.mock.calls.map((call: any[]) => call[0]);
        expect(urls).toContain('/api/projects/p1/git/branches');
    });

    it('rejects when the route answers badly, so a caller keeps an empty picker', async () => {
        const { getLocalBranches } = await import('$lib/api/git');
        mockFetch.mockResolvedValue(res(500, { error: 'boom' }));

        await expect(getLocalBranches('p1')).rejects.toThrow();
    });
});

describe('getSwitchableBranches', () => {
    it('offers only what this checkout could switch to, and hands back the task rows separately', async () => {
        const { getSwitchableBranches } = await import('$lib/api/git');
        mockFetch.mockResolvedValue(res(200, ROWS));

        const answer = await getSwitchableBranches('p1');

        expect(answer.branches).toEqual(['main', 'feature/x', 'zeta']);
        // Only a ref with a task behind it is a row someone can act on.
        expect(answer.held).toEqual([{ name: 'task/fix-the-thing-abc123', taskId: 'amber-hall' }]);
        expect(answer.withheld).toBe(2);
    });

    it('says nothing was withheld when the repository is only this checkout', async () => {
        const { getSwitchableBranches } = await import('$lib/api/git');
        mockFetch.mockResolvedValue(res(200, [
            { name: 'main' },
            { name: 'origin/main' },
        ]));

        expect(await getSwitchableBranches('p1')).toEqual({ branches: ['main'], held: [], withheld: 0 });
    });

    it('does not let a remote-tracking ref counted as held inflate the count of held work', async () => {
        const { getSwitchableBranches } = await import('$lib/api/git');
        mockFetch.mockResolvedValue(res(200, [
            { name: 'main', worktree: '' },
            { name: 'origin/next', worktree: '/somewhere' },
        ]));

        const answer = await getSwitchableBranches('p1');

        expect(answer.branches).toEqual(['main']);
        expect(answer.held, 'a remote-tracking ref is not a branch someone is working in').toEqual([]);
        expect(answer.withheld).toBe(0);
    });
});
