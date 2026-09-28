import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, cleanup, fireEvent, waitFor } from '@testing-library/svelte';
import FileBrowser from '$lib/components/FileBrowser.svelte';

/**
 * The add-project picker hides dot-prefixed names until asked: the folders a person is looking for
 * sit among the tool state a home directory is full of, and picking one is the click this dialog
 * exists for. Hiding is the component's own view of the listing, never a property of the request —
 * `/api/fs` answers the same rows either way, so flipping the option re-paints rather than refetches.
 *
 * `..` is spelled with dots and must survive the filter anyway: without it there is no way back out
 * of a subdirectory.
 */

const HOME = '/home/operator';

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
        const json = (body: unknown, status = 200) =>
            new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

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

const rowNames = (container: HTMLElement) =>
    [...container.querySelectorAll('.file-entry .name')].map(el => el.textContent);

async function openPickerAtHome() {
    const utils = render(FileBrowser, {
        props: { onClose: () => {}, onSelect: () => {}, onClone: async () => {} },
    });
    await fireEvent.click(utils.container.querySelector('.home-btn') as HTMLElement);
    await waitFor(() => expect(utils.container.querySelectorAll('.file-entry').length).toBeGreaterThan(0), { timeout: 2000 });
    return utils;
}

describe('FileBrowser — the hidden-names option', () => {
    afterEach(() => {
        cleanup();
        vi.unstubAllGlobals();
    });

    it('keeps dot-prefixed rows out of the listing, and the parent row with them', async () => {
        stubServer();
        const { container } = await openPickerAtHome();

        expect((container.querySelector('.hidden-toggle') as HTMLInputElement).checked).toBe(false);
        expect(rowNames(container)).toEqual(['..', 'src']);
    });

    it('shows the hidden rows on the listing already held, without another request', async () => {
        const fetches = stubServer();
        const { container } = await openPickerAtHome();
        const toggle = container.querySelector('.hidden-toggle') as HTMLInputElement;

        await fireEvent.click(toggle);

        expect(toggle.checked).toBe(true);
        expect(rowNames(container)).toEqual(['..', '.cache', '.config', 'src']);
        expect(fetches).toEqual([HOME]);
    });

    it('hides them again when the option is turned back off', async () => {
        stubServer();
        const { container } = await openPickerAtHome();
        const toggle = container.querySelector('.hidden-toggle') as HTMLInputElement;

        await fireEvent.click(toggle);
        await fireEvent.click(toggle);

        expect(rowNames(container)).toEqual(['..', 'src']);
    });
});
