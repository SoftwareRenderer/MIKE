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

/**
 * Downloading is the one file action the app does not perform, so what is testable here is the URL it
 * hands the browser and the label that promised it. `fetch` is stubbed and no navigation is followed.
 */

let anchorClicks: HTMLAnchorElement[] = [];

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
        return json({ error: 'a download is not a fetch' }, 404);
    }));
    // jsdom navigates on an anchor click, and the href is the assertion, so keep the element instead.
    anchorClicks = [];
    vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(function (this: HTMLAnchorElement) {
        anchorClicks.push(this);
    });
    dialogStore.toast = null;
});

afterEach(() => {
    cleanup();
    vi.restoreAllMocks();
    vi.unstubAllGlobals();
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
const menuItem = (label: string) =>
    [...(menuEl()?.querySelectorAll<HTMLButtonElement>('button') ?? [])]
        .find(b => b.querySelector('.menu-label')?.textContent === label);

async function openRowMenu(container: HTMLElement, path: string) {
    await fireEvent.contextMenu(rowEl(container, path)!, { clientX: 30, clientY: 30 });
    await waitFor(() => expect(menuEl()).toBeTruthy());
}

/** The download the last click asked for, as the browser was handed it. */
function askedDownload(): { url: URL; downloadAttribute: string | null } {
    expect(anchorClicks).toHaveLength(1);
    const anchor = anchorClicks[0];
    return { url: new URL(anchor.getAttribute('href')!, 'http://localhost'), downloadAttribute: anchor.getAttribute('download') };
}

describe('downloading from the file list', () => {
    it('asks the browser for the file the row names, under no name of the app\'s own', async () => {
        const { container } = openBrowser();
        await loadRoot(container);

        await openRowMenu(container, `${ROOT}/a.txt`);
        expect(menuItem('Download')).toBeTruthy();
        menuItem('Download')!.click();

        const { url, downloadAttribute } = askedDownload();
        expect(url.pathname).toBe('/api/fs/download');
        expect(url.searchParams.get('project')).toBe(PROJECT);
        expect(url.searchParams.get('task')).toBe(TASK);
        expect(url.searchParams.getAll('path')).toEqual([`${ROOT}/a.txt`]);
        // The server's Content-Disposition names the saved file; an attribute here would overrule it.
        expect(downloadAttribute).toBeNull();
    });

    it('promises an archive for a folder row', async () => {
        const { container } = openBrowser();
        await loadRoot(container);

        await openRowMenu(container, `${ROOT}/src`);

        expect(menuItem('Download as .tar.gz')).toBeTruthy();
        expect(menuItem('Download')).toBeFalsy();
    });

    it('sends the whole selection when the menu was opened inside one', async () => {
        const { container } = openBrowser();
        await loadRoot(container);

        await fireEvent.click(rowEl(container, `${ROOT}/a.txt`)!);
        await fireEvent.click(rowEl(container, `${ROOT}/b.txt`)!, { ctrlKey: true });
        await openRowMenu(container, `${ROOT}/a.txt`);

        menuItem('Download as .tar.gz')!.click();

        const { url } = askedDownload();
        expect(url.searchParams.getAll('path')).toEqual([`${ROOT}/a.txt`, `${ROOT}/b.txt`]);
    });

    it('names a selection holding a folder an archive, and sends both rows', async () => {
        const { container } = openBrowser();
        await loadRoot(container);

        // A ctrl-click adds to the selection instead of opening, which is how a folder gets in.
        await fireEvent.click(rowEl(container, `${ROOT}/a.txt`)!, { ctrlKey: true });
        await fireEvent.click(rowEl(container, `${ROOT}/src`)!, { ctrlKey: true });
        await openRowMenu(container, `${ROOT}/src`);

        menuItem('Download as .tar.gz')!.click();

        expect(askedDownload().url.searchParams.getAll('path')).toEqual([`${ROOT}/a.txt`, `${ROOT}/src`]);
    });

    // A refusal must not replace the listing the reader is in, so the download goes to a blank target.
    it('opens the download in a target of its own', async () => {
        const { container } = openBrowser();
        await loadRoot(container);

        await openRowMenu(container, `${ROOT}/a.txt`);
        menuItem('Download')!.click();

        expect(anchorClicks[0].target).toBe('_blank');
        expect(anchorClicks[0].rel).toBe('noopener');
    });

    it('sends no task for a listing that belongs to the project itself', async () => {
        const { container } = openBrowser({ taskId: '' });
        await loadRoot(container);

        await openRowMenu(container, `${ROOT}/a.txt`);
        menuItem('Download')!.click();

        expect(askedDownload().url.searchParams.has('task')).toBe(false);
    });

    // The phone's equivalent of the menu: no right button, so the held selection carries the action.
    it('carries the download into the selection bar', async () => {
        const { container } = openBrowser();
        await loadRoot(container);

        await fireEvent.click(rowEl(container, `${ROOT}/a.txt`)!);
        await fireEvent.click(rowEl(container, `${ROOT}/b.txt`)!, { ctrlKey: true });
        await waitFor(() => expect(container.querySelector('.file-selection-bar')).toBeTruthy());

        const bar = container.querySelector('.file-selection-bar')!;
        const button = [...bar.querySelectorAll<HTMLButtonElement>('button')]
            .find(b => b.getAttribute('aria-label') === 'Download as .tar.gz');
        expect(button).toBeTruthy();
        button!.click();

        const { url } = askedDownload();
        expect(url.searchParams.getAll('path')).toEqual([`${ROOT}/a.txt`, `${ROOT}/b.txt`]);
    });

    it('offers nothing to download on the row above the root', async () => {
        const { container } = openBrowser();
        await loadRoot(container);
        await fireEvent.click(rowEl(container, `${ROOT}/src`)!);
        await waitFor(() => expect(rowEl(container, ROOT)).toBeTruthy());

        await openRowMenu(container, ROOT);

        expect(menuItem('Download')).toBeFalsy();
        expect(menuItem('Download as .tar.gz')).toBeFalsy();
        expect(anchorClicks).toHaveLength(0);
    });
});
