import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, cleanup, fireEvent, waitFor } from '@testing-library/svelte';
import FileBrowserView from '$lib/components/FileBrowserView.svelte';
import { dialogStore } from '$lib/stores';

/**
 * A phone's Enter key does not always arrive — the one that accepts the keyboard's own candidate reports
 * `Process` — so every name row carries a commit button, and these tests press it rather than key anything.
 *
 * jsdom delivers a click to a live button whatever the focus, so the `mousedown` guard that keeps the field
 * from blurring needs its own test.
 */

const ROOT = '/srv/proj';
const PROJECT = 'proj-7';
const TASK = 'task-4';

const LISTING: Record<string, { name: string; isDir: boolean; path: string }[]> = {
    [ROOT]: [
        { name: 'docs', isDir: true, path: `${ROOT}/docs` },
        { name: 'a.txt', isDir: false, path: `${ROOT}/a.txt` },
    ],
    [`${ROOT}/docs`]: [],
};

/** Every request, query included, so a create aimed at the wrong worktree shows up. */
let posts: { pathname: string; query: string; body: any }[] = [];

beforeEach(() => {
    vi.stubGlobal('matchMedia', (query: string) => ({
        matches: false, media: query, onchange: null,
        addEventListener: () => {}, removeEventListener: () => {},
        addListener: () => {}, removeListener: () => {},
        dispatchEvent: () => false,
    }));
    posts = [];
    dialogStore.toast = null;
    vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
        const url = new URL(String(input), 'http://localhost');
        const json = (body: unknown, status = 200) =>
            new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
        if (url.pathname === '/api/auth/session') return json({ authenticated: true, csrf_token: 'tok' });
        if (url.pathname === '/api/fs') {
            return json({ path: url.searchParams.get('path'), entries: LISTING[url.searchParams.get('path') || ''] || [] });
        }
        const body = init?.body ? JSON.parse(String(init.body)) : {};
        posts.push({ pathname: url.pathname, query: url.search, body });
        // A rename answers with where the row landed; the writes and the mkdir need no body.
        if (url.pathname === '/api/fs/rename') return json({ path: `${ROOT}/${body.name}` });
        return json({});
    }));
});

afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
    posts = [];
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

/** Load the root listing: onMount is stubbed in the vitest setup, so a typed path is what loads it. */
async function loadRoot(container: HTMLElement) {
    const input = container.querySelector('.file-browser-path-input') as HTMLInputElement;
    input.value = ROOT;
    input.dispatchEvent(new Event('input', { bubbles: true }));
    await fireEvent.keyDown(input, { key: 'Enter' });
    await waitFor(() => expect(container.querySelectorAll('.file-browser-item').length).toBe(LISTING[ROOT].length));
}

const rowEl = (container: HTMLElement, path: string) =>
    container.querySelector<HTMLElement>(`.file-browser-item[title="${path}"]`);
const postedTo = (route: string) => posts.filter(p => p.pathname === route);
const nameInput = (container: HTMLElement) =>
    container.querySelector<HTMLInputElement>('.new-file-input')!;
/** The commit of whichever name row is open. */
const commitBtn = (container: HTMLElement) =>
    container.querySelector<HTMLButtonElement>('.name-row-btn.is-confirm')!;

/** Open one name row and type a name into it. */
async function openRow(container: HTMLElement, headerAria: string, name: string) {
    await fireEvent.click(container.querySelector(`button[aria-label="${headerAria}"]`) as HTMLElement);
    const input = nameInput(container);
    input.value = name;
    input.dispatchEvent(new Event('input', { bubbles: true }));
}

// Hold a row long enough to select it, which is how a finger reaches the action bar.
async function hold(row: HTMLElement) {
    await fireEvent.pointerDown(row, { pointerType: 'touch', clientX: 40, clientY: 40 });
    await new Promise(r => setTimeout(r, 550)); // LONG_PRESS_MS is 450
    await fireEvent.pointerUp(row, { pointerType: 'touch' });
}

describe('a phone commits a new name without an Enter key', () => {
    it('writes the file when the row is tapped rather than keyed', async () => {
        const { container } = openBrowser();
        await loadRoot(container);
        await openRow(container, 'Create new file', 'notes.md');

        expect(commitBtn(container).disabled).toBe(false);
        await fireEvent.click(commitBtn(container));

        await waitFor(() => expect(postedTo('/api/fs/write').length).toBe(1));
        const write = postedTo('/api/fs/write')[0];
        expect(write.body).toEqual({ path: `${ROOT}/notes.md`, content: '' });
        expect(write.query).toContain(`project=${PROJECT}`);
        expect(write.query).toContain(`task=${TASK}`);
        await waitFor(() => expect(container.querySelector('.new-file-input')).toBeNull());
    });

    it('makes the folder when its row is tapped rather than keyed', async () => {
        const { container } = openBrowser();
        await loadRoot(container);
        await openRow(container, 'Create new folder', 'docs');

        await fireEvent.click(commitBtn(container));

        await waitFor(() => expect(postedTo('/api/fs/mkdir').length).toBe(1));
        expect(postedTo('/api/fs/mkdir')[0].body).toEqual({ path: `${ROOT}/docs` });
    });

    it('waits for a name before its commit can be pressed', async () => {
        const { container } = openBrowser();
        await loadRoot(container);
        await openRow(container, 'Create new file', '   ');

        expect(commitBtn(container).disabled).toBe(true);
        await fireEvent.click(commitBtn(container));
        await new Promise(r => setTimeout(r, 30));
        expect(postedTo('/api/fs/write').length).toBe(0);
    });

    it('keeps the field focused when its own commit is pressed', async () => {
        const { container } = openBrowser();
        await loadRoot(container);
        await openRow(container, 'Create new file', 'notes.md');

        // Sent by hand because fireEvent does not hand back the event.
        const pressed = new MouseEvent('mousedown', { bubbles: true, cancelable: true });
        commitBtn(container).dispatchEvent(pressed);
        expect(pressed.defaultPrevented).toBe(true);
    });

    it('asks for nothing when the row is cancelled', async () => {
        const { container } = openBrowser();
        await loadRoot(container);
        await openRow(container, 'Create new file', 'notes.md');

        await fireEvent.click(container.querySelector('.new-file-input-row button[aria-label="Cancel"]')!);

        expect(container.querySelector('.new-file-input')).toBeNull();
        await new Promise(r => setTimeout(r, 30));
        expect(postedTo('/api/fs/write').length).toBe(0);
    });
});

describe('a held row can be renamed without a right button', () => {
    it('renames through the selection bar and the row commit', async () => {
        const { container } = openBrowser();
        await loadRoot(container);
        await hold(rowEl(container, `${ROOT}/a.txt`)!);

        const bar = container.querySelector('.file-selection-bar')!;
        await fireEvent.click(bar.querySelector('button[aria-label="Rename"]')!);

        const input = container.querySelector('.file-rename-input') as HTMLInputElement;
        input.value = 'b.txt';
        input.dispatchEvent(new Event('input', { bubbles: true }));
        await fireEvent.click(container.querySelector('.is-renaming button[aria-label="Rename"]')!);

        await waitFor(() => expect(postedTo('/api/fs/rename').length).toBe(1));
        expect(postedTo('/api/fs/rename')[0].body).toEqual({ path: `${ROOT}/a.txt`, name: 'b.txt' });
        await waitFor(() => expect(dialogStore.toast?.message).toBe('Renamed to b.txt'));
    });

    it('offers no rename while a finger holds more than one row', async () => {
        const { container } = openBrowser();
        await loadRoot(container);
        await hold(rowEl(container, `${ROOT}/a.txt`)!);
        await hold(rowEl(container, `${ROOT}/docs`)!);

        expect(container.querySelectorAll('.file-browser-item[aria-selected="true"]').length).toBe(2);
        expect(container.querySelector('.file-selection-bar button[aria-label="Rename"]')).toBeNull();
    });
});
