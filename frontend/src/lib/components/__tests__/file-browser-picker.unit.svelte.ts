import { describe, it, expect, vi, afterEach, beforeAll } from 'vitest';
import { render, cleanup, fireEvent, waitFor } from '@testing-library/svelte';
import FileBrowser from '$lib/components/FileBrowser.svelte';

/**
 * THE DIRECTORY PICKER — what one pane of `/api/fs` may show, and which of its replies is the truth.
 *
 * The picker takes a path on the SERVER's filesystem, so three rules decide what a person can pick:
 *
 *  - it is jailed to the home directory plus the declared ones, and what it shows of that limit is the
 *    server's own refusal, unchanged — that refusal is the only message naming both the roots and the
 *    widening variable;
 *  - dot-prefixed names are the picker's own view of the listing, never a property of the request:
 *    `/api/fs` answers the same rows either way, so flipping the option re-paints rather than refetches,
 *    and `..` — spelled with dots — survives the filter because it is the only way back out;
 *  - whichever reply lands LAST is the one shown, so the older load has to be refused. The replies carry
 *    no ordering guarantee, and the mount-time home listing overlaps any first navigation, so both a slow
 *    listing and a slow `/api/home` can answer over the directory the person actually chose — which is
 *    also what "Select" hands to the project create.
 *
 * `onMount` is a no-op under vitest (see vitest-setup.ts), so a test opens the home button or types a
 * path: both go through the same load.
 */

const HOME = '/home/operator';
const ENV_VAR = 'MIKE_ALLOW_ROOT_FS';
const OUTSIDE = '/etc/ssh';
const REFUSAL = `path is outside the allowed host directories: ${OUTSIDE} (allowed: ${HOME}; set ${ENV_VAR}=1 to allow any host path)`;

const SLOW = '/srv/slow-dir';
const TARGET = '/srv/git/project';

beforeAll(() => {
    vi.stubGlobal('matchMedia', (query: string) => ({
        matches: false, media: query, onchange: null,
        addEventListener: () => {}, removeEventListener: () => {},
        addListener: () => {}, removeListener: () => {},
        dispatchEvent: () => false,
    }));
});

afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
});

const json = (body: unknown, status = 200) =>
    new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

const rowNames = (container: HTMLElement) =>
    [...container.querySelectorAll('.file-entry .name')].map(el => el.textContent);

function openPicker() {
    return render(FileBrowser, {
        props: { onClose: () => {}, onSelect: () => {}, onClone: async () => {} },
    });
}

/** Open the picker on the home directory, which runs the same load `onMount` would. */
async function openPickerAtHome() {
    const utils = openPicker();
    await fireEvent.click(utils.container.querySelector('.home-btn') as HTMLElement);
    await waitFor(() => expect(utils.container.querySelectorAll('.file-entry').length).toBeGreaterThan(0), { timeout: 2000 });
    return utils;
}

async function goToPath(container: HTMLElement, path: string) {
    const input = container.querySelector('.path-input') as HTMLInputElement;
    input.value = path;
    input.dispatchEvent(new Event('input', { bubbles: true })); // Svelte's bind:value
    await fireEvent.keyDown(input, { key: 'Enter' });
}

/** Every path the component asked the server for, in order. */
const calls = () => (vi.mocked(fetch).mock.calls as any[]).map(c => String(c[0]));

describe('the host directory jail is shown by its refusal', () => {
    function stubServer() {
        vi.stubGlobal('fetch', vi.fn(async (input: string) => {
            const url = new URL(String(input), 'http://localhost');
            if (url.pathname === '/api/home') return json({ home: HOME });
            if (url.pathname === '/api/fs') {
                const path = url.searchParams.get('path') || '';
                if (path.startsWith(OUTSIDE)) return json({ error: REFUSAL }, 403);
                return json({
                    path, isGitRepo: false,
                    entries: [{ name: 'src', isDir: true, path: `${path}/src`, isGitRepo: false }],
                });
            }
            return json({ error: 'unexpected request' }, 404);
        }));
    }

    it('shows the server refusal, unchanged, for a directory outside the jail', async () => {
        stubServer();
        const { container } = await openPickerAtHome();

        await goToPath(container, OUTSIDE);

        await waitFor(() => expect(container.querySelector('.error')).toBeTruthy(), { timeout: 2000 });
        const shown = container.querySelector('.error')?.textContent ?? '';
        expect(shown).toContain(REFUSAL);
        expect(shown).toContain(ENV_VAR);
        expect(container.querySelectorAll('.file-entry').length).toBe(0);
    });
});

describe('the hidden-names option', () => {
    function stubServer() {
        const listing = [
            { name: '..', isDir: true, path: '/home', isGitRepo: false },
            { name: '.cache', isDir: true, path: `${HOME}/.cache`, isGitRepo: false },
            { name: '.config', isDir: true, path: `${HOME}/.config`, isGitRepo: false },
            { name: 'src', isDir: true, path: `${HOME}/src`, isGitRepo: false },
        ];
        const fetches: string[] = [];
        vi.stubGlobal('fetch', vi.fn(async (input: string) => {
            const url = new URL(String(input), 'http://localhost');
            if (url.pathname === '/api/home') return json({ home: HOME });
            if (url.pathname === '/api/fs') {
                const path = url.searchParams.get('path') || '';
                fetches.push(path);
                return json({ path, isGitRepo: false, entries: listing });
            }
            return json({ error: 'unexpected request' }, 404);
        }));
        return fetches;
    }

    it.each([
        { name: 'off', turns: 0, rows: ['..', 'src'] },
        { name: 'on', turns: 1, rows: ['..', '.cache', '.config', 'src'] },
        { name: 'turned back off', turns: 2, rows: ['..', 'src'] },
    ])('paints the dot-prefixed rows $name, from the listing already held', async ({ turns, rows }) => {
        const fetches = stubServer();
        const { container } = await openPickerAtHome();
        const toggle = container.querySelector('.hidden-toggle') as HTMLInputElement;

        for (let i = 0; i < turns; i++) await fireEvent.click(toggle);

        expect(toggle.checked).toBe(turns === 1);
        expect(rowNames(container)).toEqual(rows);
        // Hiding is the component's own view, so the request count never moves.
        expect(fetches).toEqual([HOME]);
    });
});

describe('a stale directory listing must not win', () => {
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

    let release: () => void = () => {};

    afterEach(() => {
        release = () => {};
    });

    /**
     * Either the `SLOW` listing blocks until `release()` — so the stale reply is guaranteed to arrive
     * after the newer one — or `/api/home` does, which makes the home listing the load that starts last.
     */
    function stubBlocking(which: 'listing' | 'home') {
        const blocked = new Promise<void>(r => { release = r; });
        vi.stubGlobal('fetch', vi.fn(async (input: string) => {
            const url = new URL(String(input), 'http://localhost');
            if (url.pathname === '/api/home') {
                if (which === 'home') await blocked;
                return json({ home: SLOW });
            }
            if (url.pathname === '/api/fs') {
                const path = url.searchParams.get('path') || '';
                if (which === 'listing' && path === SLOW) {
                    await blocked;                       // still in flight when TARGET is requested
                    return json(fsPayload(SLOW));
                }
                return json(fsPayload(path));
            }
            return json({ error: 'unexpected request' }, 404);
        }));
    }

    it('keeps the directory the user asked for when the earlier reply arrives late', async () => {
        stubBlocking('listing');
        const { container } = openPicker();

        // 1) Open SLOW — its reply is held back by the stub.
        await goToPath(container, SLOW);
        // 2) …then immediately go to TARGET, which answers at once.
        await goToPath(container, TARGET);

        await waitFor(() => expect(container.querySelector('.git-indicator')).toBeTruthy(), { timeout: 2000 });
        expect(rowNames(container)).toEqual(['main.go']);

        // 3) The stale SLOW reply arrives last. It must not replace the view.
        release();
        await new Promise(r => setTimeout(r, 200));

        expect(container.querySelector('.git-indicator'),
            'the indicator belongs to TARGET — its loss means the stale listing won').toBeTruthy();
        expect(container.querySelector('.current-path')?.textContent).toContain(TARGET);
        expect(rowNames(container)).toEqual(['main.go']);
    });

    // The other half of the same race: the home listing does not merely reply late, it starts late —
    // after the directory already chosen — so `loadDir`'s guard sees it as the newer load and lets it
    // win. What the person sees is their own path replaced by home, and "Select" greyed out.
    it('keeps the path typed while the home directory was still being looked up', async () => {
        stubBlocking('home');
        const { container } = openPicker();

        await fireEvent.click(container.querySelector('.home-btn') as Element);
        await expect.poll(calls).toContain('/api/home');

        await goToPath(container, TARGET);
        await waitFor(() => expect(container.querySelector('.git-indicator')).toBeTruthy(), { timeout: 2000 });

        release();
        await new Promise(r => setTimeout(r, 200));

        expect(container.querySelector('.git-indicator'),
            'the indicator belongs to TARGET — its loss means the home listing answered over it').toBeTruthy();
        expect((container.querySelector('.path-input') as HTMLInputElement).value).toBe(TARGET);
        expect(rowNames(container)).toEqual(['main.go']);
    });
});
