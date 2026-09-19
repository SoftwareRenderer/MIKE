import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, cleanup, fireEvent, waitFor } from '@testing-library/svelte';
import FileBrowserView from '$lib/components/FileBrowserView.svelte';
import { dialogStore } from '$lib/stores';

const ROOT = '/srv/proj';
const PROJECT = 'proj-7';
const TASK = 'task-3';

const LISTING: Record<string, { name: string; isDir: boolean; path: string }[]> = {
    [ROOT]: [
        { name: 'src', isDir: true, path: `${ROOT}/src` },
        { name: 'a.txt', isDir: false, path: `${ROOT}/a.txt` },
        { name: 'b.txt', isDir: false, path: `${ROOT}/b.txt` },
    ],
};

// Every POST the listing made, in order.
let posts: { pathname: string; query: string; body: any }[] = [];
// What each route answers. A route not set here echoes its own batch back as done.
let responders: Record<string, (body: any) => { status: number; body: unknown }> = {};

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
            posts.push({ pathname: url.pathname, query: url.search, body });
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
            taskId: TASK,
            rootPath: ROOT,
            navMode: 'path',
            unsavedFiles: [] as string[],
            pendingFsEvent: null,
            emptyMessage: 'empty',
            pendingMessage: 'pending',
            onFileSelected: () => {},
            onPathChange: () => {},
        },
    });
}

// Load the root listing. onMount is stubbed in the vitest setup, and a typed path lands on the root.
async function loadRoot(container: HTMLElement) {
    const input = container.querySelector('.file-browser-path-input') as HTMLInputElement;
    input.value = ROOT;
    input.dispatchEvent(new Event('input', { bubbles: true }));
    await fireEvent.keyDown(input, { key: 'Enter' });
    await waitFor(() => expect(container.querySelectorAll('.file-browser-item').length).toBe(3));
}

const rowEl = (container: HTMLElement, path: string) =>
    container.querySelector<HTMLElement>(`.file-browser-item[title="${path}"]`);
const menuEl = () => document.querySelector<HTMLElement>('.file-context-menu');
const menuLabels = () => [...(menuEl()?.querySelectorAll('.menu-label') ?? [])].map(e => e.textContent);
const menuItem = (label: string) =>
    [...(menuEl()?.querySelectorAll<HTMLButtonElement>('button') ?? [])]
        .find(b => b.querySelector('.menu-label')?.textContent === label);

async function openRowMenu(container: HTMLElement, path: string) {
    await fireEvent.contextMenu(rowEl(container, path)!, { clientX: 30, clientY: 30 });
    await waitFor(() => expect(menuEl()).toBeTruthy());
}

// Answer the confirmation the listing is waiting on.
async function answerDialog(answer: boolean) {
    await waitFor(() => expect(dialogStore.confirmation).toBeTruthy());
    dialogStore.confirmation?._resolve?.(answer);
    dialogStore.confirmation = null;
}

describe('the file list context menu', () => {
    it('moves the selection to the row it was opened on and offers what it can do', async () => {
        const { container } = openBrowser();
        await loadRoot(container);

        await openRowMenu(container, `${ROOT}/a.txt`);
        expect(rowEl(container, `${ROOT}/a.txt`)?.getAttribute('aria-selected')).toBe('true');
        expect(menuLabels()).toEqual(['Open', 'Cut', 'Copy', 'Rename', 'Delete']);
        expect(menuEl()?.getAttribute('aria-label')).toBe('File actions');
    });

    it('keeps a selection a right-click landed inside', async () => {
        const { container } = openBrowser();
        await loadRoot(container);

        await fireEvent.click(rowEl(container, `${ROOT}/a.txt`)!);
        await fireEvent.click(rowEl(container, `${ROOT}/b.txt`)!, { ctrlKey: true });
        await openRowMenu(container, `${ROOT}/a.txt`);
        expect(rowEl(container, `${ROOT}/b.txt`)?.getAttribute('aria-selected')).toBe('true');
        // A batch of two has no single name to rename.
        expect(menuLabels()).toEqual(['Open', 'Cut', 'Copy', 'Delete']);
    });

    it('offers a paste only once the clipboard holds something', async () => {
        const { container } = openBrowser();
        await loadRoot(container);

        await fireEvent.click(rowEl(container, `${ROOT}/a.txt`)!, { ctrlKey: true });
        await fireEvent.contextMenu(container.querySelector('.file-browser-list')!, { clientX: 60, clientY: 200 });
        await waitFor(() => expect(menuEl()).toBeTruthy());
        expect(menuLabels()).toEqual(['Cut', 'Copy', 'Rename', 'Delete']);

        menuItem('Copy')!.click();
        await waitFor(() => expect(menuEl()).toBeNull());

        await fireEvent.contextMenu(container.querySelector('.file-browser-list')!, { clientX: 60, clientY: 200 });
        await waitFor(() => expect(menuLabels()).toContain('Paste into folder'));
    });

    it('pastes a folder row into that folder, not into the folder being listed', async () => {
        const { container } = openBrowser();
        await loadRoot(container);

        await fireEvent.click(rowEl(container, `${ROOT}/a.txt`)!, { ctrlKey: true });
        await openRowMenu(container, `${ROOT}/a.txt`);
        menuItem('Copy')!.click();
        await waitFor(() => expect(menuEl()).toBeNull());

        await openRowMenu(container, `${ROOT}/src`);
        menuItem('Paste into folder')!.click();

        await waitFor(() => expect(posts.map(p => p.pathname)).toContain('/api/fs/copy'));
        const paste = posts.find(p => p.pathname === '/api/fs/copy')!;
        expect(paste.body).toEqual({ paths: [`${ROOT}/a.txt`], dest: `${ROOT}/src` });
        expect(paste.query).toContain(`project=${PROJECT}`);
        expect(paste.query).toContain(`task=${TASK}`);
    });

    it('asks before it deletes, naming every row of the selection', async () => {
        const { container } = openBrowser();
        await loadRoot(container);

        await fireEvent.click(rowEl(container, `${ROOT}/a.txt`)!);
        await fireEvent.click(rowEl(container, `${ROOT}/b.txt`)!, { ctrlKey: true });
        await openRowMenu(container, `${ROOT}/b.txt`);
        menuItem('Delete')!.click();

        await waitFor(() => expect(dialogStore.confirmation).toBeTruthy());
        expect(dialogStore.confirmation?.title).toBe('Delete files');
        expect(dialogStore.confirmation?.message).toBe('Delete these 2 items from disk? Nothing here can undo them.');
        expect(dialogStore.confirmation?.items).toEqual(['a.txt', 'b.txt']);
        expect(dialogStore.confirmation?.variant).toBe('destructive');
        expect(posts, 'nothing is removed before the answer').toEqual([]);

        await answerDialog(true);
        await waitFor(() => expect(posts.map(p => p.pathname)).toContain('/api/fs/delete'));
        expect(posts.find(p => p.pathname === '/api/fs/delete')!.body).toEqual({
            paths: [`${ROOT}/a.txt`, `${ROOT}/b.txt`],
        });
        await waitFor(() => expect(dialogStore.toast?.message).toBe('Deleted 2 items'));
    });

    it('leaves the files alone when the confirmation is refused', async () => {
        const { container } = openBrowser();
        await loadRoot(container);

        await fireEvent.click(rowEl(container, `${ROOT}/a.txt`)!);
        await openRowMenu(container, `${ROOT}/a.txt`);
        menuItem('Delete')!.click();
        await answerDialog(false);

        await new Promise(r => setTimeout(r, 30));
        expect(posts).toEqual([]);
    });

    it('says the server\'s own sentence when the whole batch is refused', async () => {
        responders = { '/api/fs/delete': () => ({ status: 403, body: { error: 'path is outside the project' } }) };
        const { container } = openBrowser();
        await loadRoot(container);

        await fireEvent.click(rowEl(container, `${ROOT}/a.txt`)!);
        await openRowMenu(container, `${ROOT}/a.txt`);
        menuItem('Delete')!.click();
        await answerDialog(true);

        await waitFor(() => expect(dialogStore.toast?.type).toBe('error'));
        expect(dialogStore.toast?.message).toBe('path is outside the project');
    });

    it('reports the item that failed when the rest of the batch landed', async () => {
        responders = {
            '/api/fs/delete': () => ({
                status: 200,
                body: { done: [{ from: `${ROOT}/a.txt` }], failed: [{ from: `${ROOT}/b.txt`, reason: 'it no longer exists' }] },
            }),
        };
        const { container } = openBrowser();
        await loadRoot(container);

        await fireEvent.click(rowEl(container, `${ROOT}/a.txt`)!);
        await fireEvent.click(rowEl(container, `${ROOT}/b.txt`)!, { ctrlKey: true });
        await openRowMenu(container, `${ROOT}/b.txt`);
        menuItem('Delete')!.click();
        await answerDialog(true);

        await waitFor(() => expect(dialogStore.toast?.type).toBe('error'));
        expect(dialogStore.toast?.message).toBe('1 item was not changed: it no longer exists');
    });
});
