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
    [`${ROOT}/src`]: [
        { name: 'main.go', isDir: false, path: `${ROOT}/src/main.go` },
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
    dialogStore.confirmationSlot.clear();
    dialogStore.toast = null;
});

afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
    dialogStore.confirmationSlot.clear();
    dialogStore.toast = null;
});

function openBrowser({ taskId = TASK }: { taskId?: string } = {}) {
    return render(FileBrowserView, {
        props: {
            projectId: PROJECT,
            taskId,
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

// A folder row is how a listing goes down one level and answers with the `..` row it prepends.
async function enterFolder(container: HTMLElement, path: string) {
    await fireEvent.click(rowEl(container, path)!);
    await waitFor(() => expect(rowEl(container, path.substring(0, path.lastIndexOf('/')))).toBeTruthy());
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
    await waitFor(() => expect(dialogStore.confirmationSlot.pending).toBeTruthy());
    dialogStore.confirmationSlot.pending?._resolve?.(answer);
    dialogStore.confirmationSlot.clear();
}

describe('the file list context menu', () => {
    it('moves the selection to the row it was opened on and offers what it can do', async () => {
        const { container } = openBrowser();
        await loadRoot(container);

        await openRowMenu(container, `${ROOT}/a.txt`);
        expect(rowEl(container, `${ROOT}/a.txt`)?.getAttribute('aria-selected')).toBe('true');
        expect(menuLabels()).toEqual(['Open', 'Open in new window', 'Cut', 'Copy', 'Rename', 'Delete']);
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
        expect(menuLabels()).toEqual(['Open', 'Open in new window', 'Cut', 'Copy', 'Delete']);
    });

    // fireEvent returns false once the handler has prevented the default, which is what keeps the
    // browser's own menu from drawing on top of this one.
    it('suppresses the browser menu when it opens on a row', async () => {
        const { container } = openBrowser();
        await loadRoot(container);

        expect(await fireEvent.contextMenu(rowEl(container, `${ROOT}/a.txt`)!, { clientX: 30, clientY: 30 })).toBe(false);
        await waitFor(() => expect(menuEl()).toBeTruthy());
    });

    it('suppresses the browser menu when it opens below the rows', async () => {
        const { container } = openBrowser();
        await loadRoot(container);

        expect(await fireEvent.contextMenu(container.querySelector('.file-browser-list')!, { clientX: 60, clientY: 200 })).toBe(false);
        await waitFor(() => expect(menuEl()).toBeTruthy());
    });

    it('suppresses the browser menu opened from the keyboard', async () => {
        const { container } = openBrowser();
        await loadRoot(container);

        expect(await fireEvent.keyDown(rowEl(container, `${ROOT}/a.txt`)!, { key: 'ContextMenu' })).toBe(false);
        await waitFor(() => expect(menuEl()).toBeTruthy());
    });

    it('leaves the browser menu to the rename input', async () => {
        const { container } = openBrowser();
        await loadRoot(container);

        await fireEvent.click(rowEl(container, `${ROOT}/a.txt`)!);
        await openRowMenu(container, `${ROOT}/a.txt`);
        menuItem('Rename')!.click();
        await waitFor(() => expect(container.querySelector('.file-rename-input')).toBeTruthy());

        expect(await fireEvent.contextMenu(container.querySelector('.file-rename-input')!)).toBe(true);
    });

    it('offers a paste only once the clipboard holds something', async () => {
        const { container } = openBrowser();
        await loadRoot(container);

        await fireEvent.click(rowEl(container, `${ROOT}/a.txt`)!, { ctrlKey: true });
        await openRowMenu(container, `${ROOT}/a.txt`);
        expect(menuLabels()).toEqual(['Open', 'Open in new window', 'Cut', 'Copy', 'Rename', 'Delete']);

        menuItem('Copy')!.click();
        await waitFor(() => expect(menuEl()).toBeNull());

        await fireEvent.contextMenu(container.querySelector('.file-browser-list')!, { clientX: 60, clientY: 200 });
        await waitFor(() => expect(menuLabels()).toEqual(['New file', 'New folder', 'Paste into folder']));
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

        await waitFor(() => expect(dialogStore.confirmationSlot.pending).toBeTruthy());
        expect(dialogStore.confirmationSlot.pending?.title).toBe('Delete files');
        expect(dialogStore.confirmationSlot.pending?.message).toBe('Delete these 2 items from disk? Nothing here can undo them.');
        expect(dialogStore.confirmationSlot.pending?.items).toEqual(['a.txt', 'b.txt']);
        expect(dialogStore.confirmationSlot.pending?.variant).toBe('destructive');
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

describe('adding something to the folder the file list shows', () => {
    /** The listing below the rows: the area a right-click asks about the folder itself. */
    async function openEmptyAreaMenu(container: HTMLElement) {
        await fireEvent.contextMenu(container.querySelector('.file-browser-list')!, { clientX: 60, clientY: 200 });
        await waitFor(() => expect(menuEl()).toBeTruthy());
    }

    // The menu item opens the header's name row — the one the header buttons open — and Enter creates.
    async function nameTheNewThing(container: HTMLElement, label: string, name: string) {
        menuItem(label)!.click();
        await waitFor(() => expect(menuEl()).toBeNull());
        await waitFor(() => expect(container.querySelector('.new-file-input')).toBeTruthy());
        const input = container.querySelector('.new-file-input') as HTMLInputElement;
        input.value = name;
        input.dispatchEvent(new Event('input', { bubbles: true }));
        await fireEvent.keyDown(input, { key: 'Enter' });
    }

    it('offers a file and a folder, and nothing about the rows a selection holds', async () => {
        const { container } = openBrowser();
        await loadRoot(container);
        await fireEvent.click(rowEl(container, `${ROOT}/a.txt`)!);

        await openEmptyAreaMenu(container);

        expect(menuLabels()).toEqual(['New file', 'New folder']);
    });

    it('writes the file it is named into the folder being listed', async () => {
        const { container } = openBrowser();
        await loadRoot(container);

        await openEmptyAreaMenu(container);
        await nameTheNewThing(container, 'New file', 'notes.md');

        await waitFor(() => expect(posts.map(p => p.pathname)).toContain('/api/fs/write'));
        const write = posts.find(p => p.pathname === '/api/fs/write')!;
        expect(write.body).toEqual({ path: `${ROOT}/notes.md`, content: '' });
        expect(write.query).toContain(`project=${PROJECT}`);
        expect(write.query).toContain(`task=${TASK}`);
    });

    it('creates the folder it is named one level down, not at the root', async () => {
        const { container } = openBrowser();
        await loadRoot(container);
        await enterFolder(container, `${ROOT}/src`);

        await openEmptyAreaMenu(container);
        await nameTheNewThing(container, 'New folder', 'docs');

        await waitFor(() => expect(posts.map(p => p.pathname)).toContain('/api/fs/mkdir'));
        const mkdir = posts.find(p => p.pathname === '/api/fs/mkdir')!;
        expect(mkdir.body).toEqual({ path: `${ROOT}/src/docs` });
        expect(mkdir.query).toContain(`task=${TASK}`);
    });
});

describe('opening a row in a new window', () => {
    // jsdom has no window.open, and the popup's geometry is not what these tests are about.
    function stubOpen() {
        const open = vi.fn((_href: string, _target: string, _features: string) => null);
        vi.stubGlobal('open', open);
        return open;
    }

    // A middle button is a `mousedown` whose default the listing cancels: the browser's own
    // autoscroll has to go, and the click event never fires for button 1.
    async function middlePress(el: Element): Promise<MouseEvent> {
        const ev = new MouseEvent('mousedown', { bubbles: true, cancelable: true, button: 1, buttons: 1 });
        await fireEvent(el, ev);
        return ev;
    }

    it('hands the task explorer a link to the file the row names', async () => {
        const open = stubOpen();
        const { container } = openBrowser();
        await loadRoot(container);

        await openRowMenu(container, `${ROOT}/a.txt`);
        menuItem('Open in new window')!.click();

        expect(open).toHaveBeenCalledTimes(1);
        expect(open.mock.calls[0][0]).toBe(`/?project=${PROJECT}&task=${TASK}&view=task-explorer&file=${ROOT}/a.txt`);
        expect(open.mock.calls[0][2].startsWith('popup=yes')).toBe(true);
    });

    it('names the project explorer when the listing belongs to no task', async () => {
        const open = stubOpen();
        const { container } = openBrowser({ taskId: '' });
        await loadRoot(container);

        await openRowMenu(container, `${ROOT}/a.txt`);
        menuItem('Open in new window')!.click();

        expect(open.mock.calls[0][0]).toBe(`/?project=${PROJECT}&view=explorer&file=${ROOT}/a.txt`);
    });

    it('marks a folder link with the slash that tells the new window to list it', async () => {
        const open = stubOpen();
        const { container } = openBrowser();
        await loadRoot(container);

        await openRowMenu(container, `${ROOT}/src`);
        menuItem('Open in new window')!.click();

        expect(open.mock.calls[0][0]).toBe(`/?project=${PROJECT}&task=${TASK}&view=task-explorer&file=${ROOT}/src/`);
    });

    it('opens on a middle click, which the row cancels out of the browser’s hands', async () => {
        const open = stubOpen();
        const { container } = openBrowser();
        await loadRoot(container);

        const press = await middlePress(rowEl(container, `${ROOT}/a.txt`)!);

        expect(press.defaultPrevented).toBe(true);
        expect(open).toHaveBeenCalledTimes(1);
        expect(open.mock.calls[0][0]).toBe(`/?project=${PROJECT}&task=${TASK}&view=task-explorer&file=${ROOT}/a.txt`);
    });

    it('is not offered for the row above the root, which no explorer will open', async () => {
        const open = stubOpen();
        const { container } = openBrowser();
        await loadRoot(container);
        await enterFolder(container, `${ROOT}/src`);

        await openRowMenu(container, ROOT);
        expect(menuLabels()).not.toContain('Open in new window');

        const press = await middlePress(rowEl(container, ROOT)!);

        expect(press.defaultPrevented).toBe(false);
        expect(open).not.toHaveBeenCalled();
    });
});
