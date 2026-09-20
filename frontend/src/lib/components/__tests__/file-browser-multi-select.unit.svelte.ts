import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest';
import { render, cleanup, fireEvent, waitFor } from '@testing-library/svelte';
import FileBrowserView from '$lib/components/FileBrowserView.svelte';

const ROOT = '/srv/proj';
const PROJECT = 'proj-7';

const LISTING: Record<string, { name: string; isDir: boolean; path: string }[]> = {
    [ROOT]: [
        { name: 'src', isDir: true, path: `${ROOT}/src` },
        { name: 'a.txt', isDir: false, path: `${ROOT}/a.txt` },
        { name: 'b.txt', isDir: false, path: `${ROOT}/b.txt` },
        { name: 'c.txt', isDir: false, path: `${ROOT}/c.txt` },
    ],
    [`${ROOT}/src`]: [
        { name: 'main.go', isDir: false, path: `${ROOT}/src/main.go` },
    ],
};

let opened: string[] = [];
let paths: string[] = [];

beforeEach(() => {
    vi.stubGlobal('matchMedia', (query: string) => ({
        matches: false, media: query, onchange: null,
        addEventListener: () => {}, removeEventListener: () => {},
        addListener: () => {}, removeListener: () => {},
        dispatchEvent: () => false,
    }));
    vi.stubGlobal('fetch', vi.fn(async (input: string) => {
        const url = new URL(String(input), 'http://localhost');
        const json = (body: unknown, status = 200) =>
            new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
        if (url.pathname === '/api/auth/session') return json({ authenticated: true, csrf_token: 'tok' });
        if (url.pathname === '/api/fs') {
            return json({ path: url.searchParams.get('path'), entries: LISTING[url.searchParams.get('path') || ''] || [] });
        }
        return json({ error: 'unexpected request' }, 404);
    }));
});

afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
    opened = [];
    paths = [];
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
            onFileSelected: (path: string) => { opened.push(path); },
            onPathChange: (path: string) => { paths.push(path); },
        },
    });
}

// The rows in the order they were rendered.
const rowEls = (container: HTMLElement) =>
    [...container.querySelectorAll<HTMLElement>('.file-browser-item')];
const rowEl = (container: HTMLElement, path: string) =>
    container.querySelector<HTMLElement>(`.file-browser-item[title="${path}"]`);
// The selected paths, read from the rows rather than from the component.
const selectedTitles = (container: HTMLElement) =>
    rowEls(container).filter(r => r.getAttribute('aria-selected') === 'true').map(r => r.title);
const bar = (container: HTMLElement) => container.querySelector<HTMLElement>('.file-selection-bar');
const list = (container: HTMLElement) => container.querySelector<HTMLElement>('.file-browser-list')!;

// Load the root listing. onMount is stubbed in the vitest setup, and a typed path lands on the root.
async function loadRoot(container: HTMLElement) {
    const input = container.querySelector('.file-browser-path-input') as HTMLInputElement;
    input.value = ROOT;
    input.dispatchEvent(new Event('input', { bubbles: true }));
    await fireEvent.keyDown(input, { key: 'Enter' });
    await waitFor(() => expect(rowEls(container).length).toBe(LISTING[ROOT].length));
}

// Click a folder row to go into it; the new listing has a `..` row to come back.
async function enterFolder(container: HTMLElement, path: string) {
    await fireEvent.click(rowEl(container, path)!);
    await waitFor(() => expect(rowEls(container).length).toBe((LISTING[path] || []).length + 1));
}

describe('the file list selection', () => {
    it('keeps the rows already held when ctrl-click adds another', async () => {
        const { container } = openBrowser();
        await loadRoot(container);

        await fireEvent.click(rowEl(container, `${ROOT}/a.txt`)!);
        expect(opened).toEqual([`${ROOT}/a.txt`]);
        expect(selectedTitles(container)).toEqual([`${ROOT}/a.txt`]);
        // One selected row is not a batch, so the action bar stays hidden.
        expect(bar(container)).toBeNull();

        await fireEvent.click(rowEl(container, `${ROOT}/c.txt`)!, { ctrlKey: true });
        expect(selectedTitles(container)).toEqual([`${ROOT}/a.txt`, `${ROOT}/c.txt`]);
        expect(opened, 'a ctrl-click selects, it does not open').toEqual([`${ROOT}/a.txt`]);

        const barEl = bar(container);
        expect(barEl, 'a selection needs the touch path into its actions').toBeTruthy();
        expect(barEl!.querySelector('.selection-count')?.textContent).toBe('2 selected');
        expect([...barEl!.querySelectorAll('button')].map(b => b.getAttribute('aria-label')))
            .toEqual(['Cut', 'Copy', 'Delete', 'Clear selection']);
    });

    it('takes the range between the anchor and the row a shift-click lands on', async () => {
        const { container } = openBrowser();
        await loadRoot(container);

        await fireEvent.click(rowEl(container, `${ROOT}/a.txt`)!);
        await fireEvent.click(rowEl(container, `${ROOT}/c.txt`)!, { shiftKey: true });
        expect(selectedTitles(container)).toEqual([`${ROOT}/a.txt`, `${ROOT}/b.txt`, `${ROOT}/c.txt`]);
    });

    it('starts a shift range at the row clicked when nothing anchored it', async () => {
        const { container } = openBrowser();
        await loadRoot(container);

        await fireEvent.click(rowEl(container, `${ROOT}/b.txt`)!, { shiftKey: true });
        expect(selectedTitles(container)).toEqual([`${ROOT}/b.txt`]);
        // A plain click starts a new selection instead of extending the range.
        await fireEvent.click(rowEl(container, `${ROOT}/a.txt`)!);
        expect(selectedTitles(container)).toEqual([`${ROOT}/a.txt`]);
    });

    it('never selects the folder the listing is in', async () => {
        const { container } = openBrowser();
        await loadRoot(container);
        await enterFolder(container, `${ROOT}/src`);
        expect(rowEl(container, ROOT)?.querySelector('.file-name')?.textContent).toBe('..');

        await fireEvent.click(rowEl(container, ROOT)!, { ctrlKey: true });
        expect(selectedTitles(container)).toEqual([]);
        expect(bar(container)).toBeNull();

        // It still navigates: refusing to select it must not cost the click.
        await fireEvent.click(rowEl(container, ROOT)!);
        expect(paths[paths.length - 1]).toBe(ROOT);
    });

    it('selects a row held under a finger without opening it', async () => {
        const { container } = openBrowser();
        await loadRoot(container);
        const row = rowEl(container, `${ROOT}/b.txt`)!;

        await fireEvent.pointerDown(row, { pointerType: 'touch', clientX: 40, clientY: 40 });
        await new Promise(r => setTimeout(r, 550)); // LONG_PRESS_MS is 450

        expect(selectedTitles(container)).toEqual([`${ROOT}/b.txt`]);
        expect(bar(container)?.querySelector('.selection-count')?.textContent).toBe('1 selected');

        // The long press ends in a click, which must not open the row it selected.
        await fireEvent.click(row, { pointerType: 'touch' });
        expect(opened).toEqual([]);
        expect(selectedTitles(container)).toEqual([`${ROOT}/b.txt`]);
    });

    it('clears the selection on Escape without handing the key to the panel behind it', async () => {
        const { container } = openBrowser();
        await loadRoot(container);
        const seenByTheApp = vi.fn();
        document.addEventListener('keydown', seenByTheApp);

        await fireEvent.keyDown(list(container), { key: 'a', ctrlKey: true });
        expect(selectedTitles(container)).toEqual([`${ROOT}/src`, `${ROOT}/a.txt`, `${ROOT}/b.txt`, `${ROOT}/c.txt`]);
        seenByTheApp.mockClear();

        await fireEvent.keyDown(list(container), { key: 'Escape' });
        expect(selectedTitles(container)).toEqual([]);
        expect(bar(container)).toBeNull();
        expect(seenByTheApp, 'the explorer closes its panels on the same key').not.toHaveBeenCalled();
    });
});
