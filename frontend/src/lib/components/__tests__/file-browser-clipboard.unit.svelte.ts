import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, cleanup, fireEvent, waitFor } from '@testing-library/svelte';
import FileBrowserView from '$lib/components/FileBrowserView.svelte';
import { dialogStore } from '$lib/stores';

const ROOT = '/srv/proj';
const PROJECT = 'proj-7';

type ListedEntry = { name: string; isDir: boolean; path: string };

let LISTING: Record<string, ListedEntry[]> = {
    [ROOT]: [
        { name: 'a.txt', isDir: false, path: `${ROOT}/a.txt` },
        { name: 'b.txt', isDir: false, path: `${ROOT}/b.txt` },
    ],
};

/** A listing with a folder whose own listing is empty, so navigating into it removes the focused row. */
const NAV_LISTING: Record<string, ListedEntry[]> = {
    [ROOT]: [
        { name: 'docs', isDir: true, path: `${ROOT}/docs` },
        { name: 'empty', isDir: true, path: `${ROOT}/empty` },
        { name: 'a.txt', isDir: false, path: `${ROOT}/a.txt` },
        { name: 'b.txt', isDir: false, path: `${ROOT}/b.txt` },
    ],
    [`${ROOT}/docs`]: [{ name: 'c.md', isDir: false, path: `${ROOT}/docs/c.md` }],
    [`${ROOT}/empty`]: [],
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
    dialogStore.confirmationSlot.clear();
    dialogStore.toast = null;
});

afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
    dialogStore.confirmationSlot.clear();
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
    const rows = LISTING[ROOT].length;
    const input = container.querySelector('.file-browser-path-input') as HTMLInputElement;
    input.value = ROOT;
    input.dispatchEvent(new Event('input', { bubbles: true }));
    await fireEvent.keyDown(input, { key: 'Enter' });
    await waitFor(() => expect(container.querySelectorAll('.file-browser-item').length).toBe(rows));
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

        await waitFor(() => expect(dialogStore.confirmationSlot.pending?.title).toBe('Pasted under other names'));
        expect(dialogStore.confirmationSlot.pending?.message).toBe('That folder already held these names:');
        expect(dialogStore.confirmationSlot.pending?.items).toEqual(['a.txt → a copy.txt']);
        // Nothing was destroyed, so the dialog offers one button.
        expect(dialogStore.confirmationSlot.pending?.okText).toBe('OK');
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
        await waitFor(() => expect(dialogStore.confirmationSlot.pending?.items).toEqual(['a.txt → a copy.txt']));
    });
});

/** Press a key where the browser would: on the element holding focus. */
async function pressKey(key: string, mods: { ctrl?: boolean } = {}) {
    return fireEvent.keyDown(document.activeElement as HTMLElement, { key, ctrlKey: mods.ctrl === true });
}

describe('the file list keyboard, wherever the panel holds focus', () => {
    beforeEach(() => {
        LISTING = NAV_LISTING;
    });

    /** Press a row, having focused it first the way a mouse press does. */
    async function clickRow(container: HTMLElement, path: string) {
        const row = rowEl(container, path)!;
        row.focus();
        await fireEvent.click(row);
    }

    it('cuts and pastes while the panel header holds focus', async () => {
        const { container } = openBrowser();
        await loadRoot(container);

        await clickRow(container, `${ROOT}/a.txt`);
        const upload = container.querySelector<HTMLElement>('.file-actions .new-file-btn')!;
        upload.focus();
        expect(document.activeElement).toBe(upload);

        await pressKey('x', { ctrl: true });
        expect(rowEl(container, `${ROOT}/a.txt`)?.className).toContain('is-cut');

        await pressKey('v', { ctrl: true });
        await waitFor(() => expect(postedTo('/api/fs/move').length).toBe(1));
        expect(postedTo('/api/fs/move')[0]).toEqual({ paths: [`${ROOT}/a.txt`], dest: ROOT });
    });

    it('keeps the clipboard keys when navigating into a folder removes the focused row', async () => {
        const { container } = openBrowser();
        await loadRoot(container);

        await clickRow(container, `${ROOT}/a.txt`);
        await pressKey('x', { ctrl: true });
        expect(rowEl(container, `${ROOT}/a.txt`)?.className).toContain('is-cut');

        // Directories are listed first, so this row is inside a folder that will hold nothing but the
        // folder above: the focused row is gone.
        await clickRow(container, `${ROOT}/empty`);
        await waitFor(() => expect(container.querySelectorAll('.file-browser-item').length).toBe(1));
        expect(document.activeElement).toBe(listEl(container));

        await pressKey('v', { ctrl: true });
        await waitFor(() => expect(postedTo('/api/fs/move').length).toBe(1));
        expect(postedTo('/api/fs/move')[0]).toEqual({ paths: [`${ROOT}/a.txt`], dest: `${ROOT}/empty` });
    });

    it('cuts the row the key arrived on when nothing is selected', async () => {
        const { container } = openBrowser();
        await loadRoot(container);

        rowEl(container, `${ROOT}/a.txt`)!.focus();
        await pressKey('x', { ctrl: true });
        expect(rowEl(container, `${ROOT}/a.txt`)?.className).toContain('is-cut');

        await pressKey('v', { ctrl: true });
        await waitFor(() => expect(postedTo('/api/fs/move').length).toBe(1));
        expect(postedTo('/api/fs/move')[0]).toEqual({ paths: [`${ROOT}/a.txt`], dest: ROOT });
    });

    it('copies the row the key arrived on when nothing is selected', async () => {
        const { container } = openBrowser();
        await loadRoot(container);

        rowEl(container, `${ROOT}/b.txt`)!.focus();
        await pressKey('c', { ctrl: true });
        expect(rowEl(container, `${ROOT}/b.txt`)?.className).not.toContain('is-cut');

        await pressKey('v', { ctrl: true });
        await waitFor(() => expect(postedTo('/api/fs/copy').length).toBe(1));
        expect(postedTo('/api/fs/copy')[0]).toEqual({ paths: [`${ROOT}/b.txt`], dest: ROOT });
    });

    it('renames the row the key arrived on when nothing is selected', async () => {
        const { container } = openBrowser();
        await loadRoot(container);

        rowEl(container, `${ROOT}/a.txt`)!.focus();
        await pressKey('F2');

        const input = container.querySelector<HTMLInputElement>('.file-rename-input');
        expect(input?.value).toBe('a.txt');
        expect(posts).toEqual([]);
    });

    it('ignores a clipboard key aimed at the folder above', async () => {
        const { container } = openBrowser();
        await loadRoot(container);

        await clickRow(container, `${ROOT}/docs`);
        await waitFor(() => expect(container.querySelectorAll('.file-browser-item').length).toBe(2));

        // The folder above is titled with the folder it leads to.
        rowEl(container, ROOT)!.focus();
        await pressKey('x', { ctrl: true });
        await pressKey('v', { ctrl: true });

        expect(container.querySelector('.is-cut')).toBeNull();
        expect(postedTo('/api/fs/move')).toEqual([]);
    });

    it('leaves the clipboard keys to the browser while the path field holds focus', async () => {
        const { container } = openBrowser();
        await loadRoot(container);

        await clickRow(container, `${ROOT}/a.txt`);
        const pathField = container.querySelector<HTMLElement>('.file-browser-path-input')!;
        pathField.focus();
        await pressKey('x', { ctrl: true });
        await pressKey('v', { ctrl: true });

        expect(rowEl(container, `${ROOT}/a.txt`)?.className).not.toContain('is-cut');
        expect(posts).toEqual([]);
    });

    it('leaves the clipboard keys to the browser while a name is being typed', async () => {
        const { container } = openBrowser();
        await loadRoot(container);

        rowEl(container, `${ROOT}/a.txt`)!.focus();
        await pressKey('F2');
        const input = container.querySelector<HTMLInputElement>('.file-rename-input')!;
        expect(input.value).toBe('a.txt');
        await pressKey('x', { ctrl: true });
        await pressKey('v', { ctrl: true });

        expect(input.isConnected).toBe(true);
        expect(container.querySelector('.is-cut')).toBeNull();
        expect(posts).toEqual([]);
    });

    it('leaves the keys to the context menu while it is open', async () => {
        const { container } = openBrowser();
        await loadRoot(container);

        const row = rowEl(container, `${ROOT}/a.txt`)!;
        await fireEvent.contextMenu(row, { clientX: 60, clientY: 120 });
        const item = container.querySelector<HTMLElement>('.file-context-menu .menu-item')!;
        item.focus();
        await pressKey('Delete');

        expect(dialogStore.confirmationSlot.pending).toBeNull();
        expect(posts).toEqual([]);
    });
});
