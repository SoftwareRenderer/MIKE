import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, cleanup, fireEvent, waitFor } from '@testing-library/svelte';
import FileBrowserView from '$lib/components/FileBrowserView.svelte';
import { dialogStore } from '$lib/stores';

const ROOT = '/srv/proj';
const PROJECT = 'proj-7';

const LISTING: Record<string, { name: string; isDir: boolean; path: string }[]> = {
    [ROOT]: [
        { name: 'a.txt', isDir: false, path: `${ROOT}/a.txt` },
        { name: 'b.txt', isDir: false, path: `${ROOT}/b.txt` },
    ],
};

let posts: { pathname: string; body: any }[] = [];
let responders: Record<string, (body: any) => { status: number; body: unknown }> = {};
let deleted: string[][] = [];

beforeEach(() => {
    vi.stubGlobal('matchMedia', (query: string) => ({
        matches: false, media: query, onchange: null,
        addEventListener: () => {}, removeEventListener: () => {},
        addListener: () => {}, removeListener: () => {},
        dispatchEvent: () => false,
    }));
    vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
        const url = new URL(String(input), 'http://localhost');
        const json = (body: unknown, status = 200) =>
            new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
        if (url.pathname === '/api/auth/session') return json({ authenticated: true, csrf_token: 'tok' });
        if (url.pathname === '/api/fs') {
            return json({ path: url.searchParams.get('path'), entries: LISTING[url.searchParams.get('path') || ''] || [] });
        }
        if (init?.method === 'POST') {
            const body = JSON.parse(String(init.body));
            posts.push({ pathname: url.pathname, body });
            const answer = responders[url.pathname]?.(body) ?? {
                status: 200,
                body: { done: (body.paths || []).map((p: string) => ({ from: p })), failed: [] },
            };
            return json(answer.body, answer.status);
        }
        return json({ error: 'unexpected request' }, 404);
    }));
    posts = [];
    responders = {};
    deleted = [];
    dialogStore.confirmation = null;
    dialogStore.toast = null;
});

afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
    dialogStore.confirmation = null;
    dialogStore.toast = null;
});

function openBrowser() {
    return render(FileBrowserView, {
        props: {
            projectId: PROJECT,
            rootPath: ROOT,
            navMode: 'path',
            unsavedFiles: [] as string[],
            pendingFsEvent: null,
            emptyMessage: 'empty',
            pendingMessage: 'pending',
            onFileSelected: () => {},
            onPathChange: () => {},
            onDeleted: (paths: string[]) => { deleted.push(paths); },
        },
    });
}

/** Load the root listing. onMount is stubbed in the vitest setup, and a typed path lands on the root. */
async function loadRoot(container: HTMLElement) {
    const input = container.querySelector('.file-browser-path-input') as HTMLInputElement;
    input.value = ROOT;
    input.dispatchEvent(new Event('input', { bubbles: true }));
    await fireEvent.keyDown(input, { key: 'Enter' });
    await waitFor(() => expect(container.querySelectorAll('.file-browser-item').length).toBe(2));
}

const rowEl = (container: HTMLElement, path: string) =>
    container.querySelector<HTMLElement>(`.file-browser-item[title="${path}"]`);
const listEl = (container: HTMLElement) => container.querySelector<HTMLElement>('.file-browser-list')!;
const postedTo = (route: string) => posts.filter(p => p.pathname === route).map(p => p.body);

describe('the file list clipboard and rename', () => {
    it('lists every file a paste had to rename', async () => {
        responders = {
            '/api/fs/copy': (body) => ({
                status: 200,
                body: {
                    done: [
                        { from: body.paths[0], to: `${ROOT}/a copy.txt`, renamed: true },
                        { from: body.paths[1], to: `${ROOT}/b.txt` },
                    ],
                    failed: [],
                },
            }),
        };
        const { container } = openBrowser();
        await loadRoot(container);

        await fireEvent.click(rowEl(container, `${ROOT}/a.txt`)!);
        await fireEvent.click(rowEl(container, `${ROOT}/b.txt`)!, { ctrlKey: true });
        await fireEvent.keyDown(listEl(container), { key: 'c', ctrlKey: true });
        await fireEvent.keyDown(listEl(container), { key: 'v', ctrlKey: true });

        await waitFor(() => expect(dialogStore.confirmation?.title).toBe('Pasted under other names'));
        expect(dialogStore.confirmation?.message).toBe('That folder already held these names:');
        expect(dialogStore.confirmation?.items).toEqual(['a.txt → a copy.txt']);
        // Nothing was destroyed, so the dialog offers one button.
        expect(dialogStore.confirmation?.okText).toBe('OK');
    });

    it('marks the cut rows, moves them on paste, and forgets them', async () => {
        const { container } = openBrowser();
        await loadRoot(container);

        await fireEvent.click(rowEl(container, `${ROOT}/a.txt`)!);
        await fireEvent.keyDown(listEl(container), { key: 'x', ctrlKey: true });
        expect(rowEl(container, `${ROOT}/a.txt`)?.className).toContain('is-cut');

        await fireEvent.keyDown(listEl(container), { key: 'v', ctrlKey: true });
        await waitFor(() => expect(postedTo('/api/fs/move').length).toBe(1));
        expect(postedTo('/api/fs/move')[0]).toEqual({ paths: [`${ROOT}/a.txt`], dest: ROOT });
        await waitFor(() => expect(deleted).toEqual([[`${ROOT}/a.txt`]]));
        await waitFor(() => expect(dialogStore.toast?.message).toBe('Pasted 1 item'));

        // A second paste must not move the file again. The listing is still reloading, so this
        // paste is refused only after the first one finishes.
        await new Promise(r => setTimeout(r, 100));
        await fireEvent.keyDown(listEl(container), { key: 'v', ctrlKey: true });
        await new Promise(r => setTimeout(r, 30));
        expect(postedTo('/api/fs/move').length).toBe(1);
    });

    it('keeps a copy available for another paste', async () => {
        const { container } = openBrowser();
        await loadRoot(container);

        await fireEvent.click(rowEl(container, `${ROOT}/a.txt`)!);
        await fireEvent.keyDown(listEl(container), { key: 'c', ctrlKey: true });
        await fireEvent.keyDown(listEl(container), { key: 'v', ctrlKey: true });
        await waitFor(() => expect(postedTo('/api/fs/copy').length).toBe(1));
        await new Promise(r => setTimeout(r, 100)); // the listing's reload ends the mutation
        await fireEvent.keyDown(listEl(container), { key: 'v', ctrlKey: true });
        await waitFor(() => expect(postedTo('/api/fs/copy').length).toBe(2));
        expect(rowEl(container, `${ROOT}/a.txt`)?.className).not.toContain('is-cut');
    });

    it('renames in the row and hands the old name to whoever holds the editor', async () => {
        responders = {
            '/api/fs/rename': (body) => ({ status: 200, body: { path: `${ROOT}/${body.name}` } }),
        };
        const { container } = openBrowser();
        await loadRoot(container);

        await fireEvent.click(rowEl(container, `${ROOT}/a.txt`)!);
        await fireEvent.keyDown(listEl(container), { key: 'F2' });

        const input = container.querySelector<HTMLInputElement>('.file-rename-input')!;
        expect(input.value).toBe('a.txt');
        input.value = 'notes.md';
        input.dispatchEvent(new Event('input', { bubbles: true }));
        await fireEvent.keyDown(input, { key: 'Enter' });

        await waitFor(() => expect(postedTo('/api/fs/rename').length).toBe(1));
        expect(postedTo('/api/fs/rename')[0]).toEqual({ path: `${ROOT}/a.txt`, name: 'notes.md' });
        await waitFor(() => expect(deleted).toEqual([[`${ROOT}/a.txt`]]));
        await waitFor(() => expect(dialogStore.toast?.message).toBe('Renamed to notes.md'));
    });

    it('writes nothing when the rename is cancelled', async () => {
        const { container } = openBrowser();
        await loadRoot(container);

        await fireEvent.click(rowEl(container, `${ROOT}/a.txt`)!);
        await fireEvent.keyDown(listEl(container), { key: 'F2' });
        const input = container.querySelector<HTMLInputElement>('.file-rename-input')!;
        input.value = 'notes.md';
        input.dispatchEvent(new Event('input', { bubbles: true }));
        await fireEvent.keyDown(input, { key: 'Escape' });

        await waitFor(() => expect(container.querySelector('.file-rename-input')).toBeNull());
        expect(rowEl(container, `${ROOT}/a.txt`)?.querySelector('.file-name')?.textContent).toBe('a.txt');
        expect(posts).toEqual([]);
    });

    it('says which item did not move when the rest of the paste landed', async () => {
        responders = {
            '/api/fs/copy': (body) => ({
                status: 200,
                body: {
                    done: [{ from: body.paths[0], to: `${ROOT}/a copy.txt`, renamed: true }],
                    failed: [{ from: body.paths[1], reason: 'the server could not do it' }],
                },
            }),
        };
        const { container } = openBrowser();
        await loadRoot(container);

        await fireEvent.click(rowEl(container, `${ROOT}/a.txt`)!);
        await fireEvent.click(rowEl(container, `${ROOT}/b.txt`)!, { ctrlKey: true });
        await fireEvent.keyDown(listEl(container), { key: 'c', ctrlKey: true });
        await fireEvent.keyDown(listEl(container), { key: 'v', ctrlKey: true });

        await waitFor(() => expect(dialogStore.toast?.type).toBe('error'));
        expect(dialogStore.toast?.message).toBe('1 item was not changed: the server could not do it');
        // The renamed notice still shows behind the failure.
        await waitFor(() => expect(dialogStore.confirmation?.items).toEqual(['a.txt → a copy.txt']));
    });
});
