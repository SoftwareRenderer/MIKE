import { describe, it, expect, vi, afterEach, beforeAll } from 'vitest';
import { render, cleanup, fireEvent, waitFor } from '@testing-library/svelte';
import FileBrowser from '$lib/components/FileBrowser.svelte';

/**
 * FileBrowser lists a directory over `/api/fs`, and whichever reply lands LAST
 * is the one the UI shows. Two loads overlap routinely: the user opens a path
 * (or clicks a folder) and then types/picks another one before the first reply
 * arrives — the mount-time home listing does this against any first navigation
 * too. The replies have no ordering guarantee.
 *
 * Before the sequence guard in `loadDir`, a slow reply for the *earlier* path
 * landed last and overwrote the directory the user actually asked for: the list,
 * the git indicator, and the enabled/disabled state of "Select" then described a
 * different directory than the one shown in the path bar — and than `currentPath`,
 * which is exactly what "Select" hands to the project create. Pinned here: the
 * stale reply is still delivered, it just no longer wins.
 */

const SLOW = '/srv/slow-dir';
const TARGET = '/srv/git/project';

/** Only TARGET is a git repo, so the indicator identifies which listing won. */
function fsPayload(path: string) {
    const isTarget = path === TARGET;
    return {
        path,
        isGitRepo: isTarget,
        entries: isTarget
            ? [{ name: 'main.go', isDir: false, path: `${TARGET}/main.go`, isGitRepo: false }]
            : [{ name: 'stale-dir', isDir: true, path: `${SLOW}/stale-dir`, isGitRepo: false }],
    };
}

let releaseSlow: () => void = () => {};
let releaseHome: () => void = () => {};

beforeAll(() => {
    vi.stubGlobal('matchMedia', (query: string) => ({
        matches: false, media: query, onchange: null,
        addEventListener: () => {}, removeEventListener: () => {},
        addListener: () => {}, removeListener: () => {},
        dispatchEvent: () => false,
    }));
});

/** The `SLOW` listing blocks until `releaseSlow()`; everything else answers at
 *  once — so the stale reply is guaranteed to arrive after the newer one. */
function stubOverlappingFetch() {
    const blocked = new Promise<void>(r => { releaseSlow = r; });
    vi.stubGlobal('fetch', vi.fn(async (input: string) => {
        const url = new URL(String(input), 'http://localhost');
        const json = (body: unknown, status = 200) =>
            new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

        if (url.pathname === '/api/home') return json({ home: SLOW });

        if (url.pathname === '/api/fs') {
            const path = url.searchParams.get('path') || '';
            if (path === SLOW) {
                await blocked;                       // still in flight when TARGET is requested
                return json(fsPayload(SLOW));
            }
            return json(fsPayload(path));
        }
        return json({ error: 'unexpected request' }, 404);
    }));
}

/** The `/api/home` round trip blocks until `releaseHome()`; every listing answers at once, so the
 *  home listing is the one that starts last and the one a path typed in the meantime must survive. */
function stubSlowHome() {
    const blocked = new Promise<void>(r => { releaseHome = r; });
    vi.stubGlobal('fetch', vi.fn(async (input: string) => {
        const url = new URL(String(input), 'http://localhost');
        const json = (body: unknown, status = 200) =>
            new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

        if (url.pathname === '/api/home') {
            await blocked;
            return json({ home: SLOW });
        }
        if (url.pathname === '/api/fs') return json(fsPayload(url.searchParams.get('path') || ''));
        return json({ error: 'unexpected request' }, 404);
    }));
}

async function goToPath(container: HTMLElement, path: string) {
    const input = container.querySelector('.path-input') as HTMLInputElement;
    input.value = path;
    input.dispatchEvent(new Event('input', { bubbles: true })); // Svelte's bind:value
    await fireEvent.keyDown(input, { key: 'Enter' });
}

const listed = (container: HTMLElement) =>
    [...container.querySelectorAll('.file-entry .name')].map(e => e.textContent);

/** Every path the component asked the server for, in order. */
const calls = () => (vi.mocked(fetch).mock.calls as any[]).map(c => String(c[0]));

describe('FileBrowser — a stale directory listing must not win', () => {
    afterEach(() => {
        cleanup();
        vi.unstubAllGlobals();
        releaseSlow = () => {};
    });

    it('keeps the directory the user asked for when the earlier reply arrives late', async () => {
        stubOverlappingFetch();
        const { container } = render(FileBrowser, {
            props: { onClose: () => {}, onSelect: () => {}, onClone: async () => {} },
        });

        // 1) Open SLOW — its reply is held back by the stub.
        await goToPath(container, SLOW);
        // 2) …then immediately go to TARGET, which answers at once.
        await goToPath(container, TARGET);

        await waitFor(() => expect(container.querySelector('.git-indicator')).toBeTruthy(), { timeout: 2000 });
        expect(listed(container)).toContain('main.go');

        // 3) The stale SLOW reply arrives last. It must not replace the view.
        releaseSlow();
        await new Promise(r => setTimeout(r, 200));

        expect(container.querySelector('.git-indicator'),
            'the indicator belongs to TARGET — its loss means the stale listing won').toBeTruthy();
        expect(container.querySelector('.current-path')?.textContent).toContain(TARGET);
        expect(listed(container)).toEqual(['main.go']);
    });

    it('keeps the path typed while the home directory was still being looked up', async () => {
        // The other half of the same race: the home listing does not merely reply late, it starts late —
        // after the directory already chosen — so `loadDir`'s guard sees it as the newer load and lets it
        // win. What the person sees is their own path replaced by home, and "Select" greyed out. The home
        // button runs the same lookup the mount does, so it is how the window is opened here.
        stubSlowHome();
        const { container } = render(FileBrowser, {
            props: { onClose: () => {}, onSelect: () => {}, onClone: async () => {} },
        });

        await fireEvent.click(container.querySelector('.home-btn') as Element);
        await expect.poll(calls).toContain('/api/home');

        await goToPath(container, TARGET);
        await waitFor(() => expect(container.querySelector('.git-indicator')).toBeTruthy(), { timeout: 2000 });

        releaseHome();
        await new Promise(r => setTimeout(r, 200));

        expect(container.querySelector('.git-indicator'),
            'the indicator belongs to TARGET — its loss means the home listing answered over it').toBeTruthy();
        expect((container.querySelector('.path-input') as HTMLInputElement).value).toBe(TARGET);
        expect(listed(container)).toEqual(['main.go']);
    });
});
