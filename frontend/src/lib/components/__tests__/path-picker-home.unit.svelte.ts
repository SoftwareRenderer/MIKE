import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, cleanup, waitFor } from '@testing-library/svelte';
import PathPicker from '$lib/components/PathPicker.svelte';

/**
 * A host-directory picker opens where the server can actually answer: `GET /api/fs` with no project
 * is jailed to the operator's home, so a picker that opened on a fixed `/home` opens on that jail's
 * refusal wherever the home is not under it. `/api/home` is what reports the directory, and `/home`
 * is only where the picker lands when that lookup itself fails.
 */

const HOME = '/home/operator';
const PROJECT = '/srv/work/checkout';

/** Stub `/api/home` and the listing, and record every path the picker asks to list. */
function stubServer({ home = HOME, homeFails = false }: { home?: string; homeFails?: boolean } = {}) {
    const listed: string[] = [];
    const askedHome = { count: 0 };
    vi.stubGlobal('fetch', vi.fn(async (input: string) => {
        const url = new URL(String(input), 'http://localhost');
        const json = (body: unknown, status = 200) =>
            new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

        if (url.pathname === '/api/home') {
            askedHome.count++;
            return homeFails ? json({ error: 'internal error' }, 500) : json({ home });
        }
        if (url.pathname === '/api/fs') {
            const path = url.searchParams.get('path') || '';
            listed.push(path);
            return json({
                path, isGitRepo: false,
                entries: [{ name: 'src', isDir: true, path: `${path}/src`, isGitRepo: false }],
            });
        }
        return json({ error: 'unexpected request' }, 404);
    }));
    return { listed, askedHome };
}

function open(props: Record<string, unknown> = {}) {
    return render(PathPicker, {
        props: {
            title: 'Select a directory',
            emptyLabel: 'No subdirectories',
            selectTitle: 'Select this directory',
            onClose: () => {},
            onSelect: () => {},
            ...props,
        },
    });
}

/** Wait for the opening listing to paint, so the request that produced it has been made. */
async function opened(utils: ReturnType<typeof open>) {
    await waitFor(() => expect(utils.container.querySelectorAll('.file-entry').length).toBe(1), { timeout: 2000 });
    return utils;
}

const pathInput = (container: HTMLElement) => (container.querySelector('.path-input') as HTMLInputElement).value;

describe('PathPicker — the directory it opens on', () => {
    afterEach(() => {
        cleanup();
        vi.unstubAllGlobals();
    });

    it('opens on the server home the caller was not told', async () => {
        const { listed } = stubServer();

        const utils = await opened(open());

        expect(listed).toEqual([HOME]);
        expect(pathInput(utils.container)).toBe(HOME);
    });

    it('opens on /home when the server cannot report one', async () => {
        const { listed } = stubServer({ homeFails: true });

        await opened(open());

        expect(listed).toEqual(['/home']);
    });

    it('opens on the path it is given and never asks for a home', async () => {
        const { listed, askedHome } = stubServer();

        const utils = await opened(open({ initialPath: PROJECT }));

        expect(listed).toEqual([PROJECT]);
        expect(pathInput(utils.container)).toBe(PROJECT);
        expect(askedHome.count).toBe(0);
    });
});
