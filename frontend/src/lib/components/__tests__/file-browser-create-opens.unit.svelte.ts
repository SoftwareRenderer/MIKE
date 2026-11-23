import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, cleanup, fireEvent, waitFor } from '@testing-library/svelte';
import { tick } from 'svelte';
import FileBrowserView from '$lib/components/FileBrowserView.svelte';

/**
 * A file the person created is the file they are about to type into: the listing hands it to the screen
 * (`onFileCreated`) instead of merely listing it, and writes it as a create, so a name that already holds
 * something is refused rather than emptied. A folder has no buffer to type into, so it is only opened
 * into — and the keyboard the name row held stays put long enough for the caret to arrive.
 */

const ROOT = '/srv/proj';
const PROJECT = 'proj-7';
const TASK = 'task-4';

let entriesFor: Record<string, { name: string; isDir: boolean; path: string }[]> = {};

/** Every write the component asked for, so the create's intent is visible. */
let writes: { query: string; body: any }[] = [];
let mkdirs: { body: any }[] = [];
/** What the create routes answer; tests set this to make a create fail. */
let writeStatus = 200;
let writeBody: unknown = {};

beforeEach(() => {
    vi.stubGlobal('matchMedia', (query: string) => ({
        matches: false, media: query, onchange: null,
        addEventListener: () => {}, removeEventListener: () => {},
        addListener: () => {}, removeListener: () => {},
        dispatchEvent: () => false,
    }));
    entriesFor = {
        [ROOT]: [
            { name: 'docs', isDir: true, path: `${ROOT}/docs` },
            { name: 'a.txt', isDir: false, path: `${ROOT}/a.txt` },
        ],
        [`${ROOT}/notes`]: [],
    };
    writes = [];
    mkdirs = [];
    writeStatus = 200;
    writeBody = {};
    vi.stubGlobal('fetch', vi.fn(async (input: string, init?: RequestInit) => {
        const url = new URL(String(input), 'http://localhost');
        const json = (body: unknown, status = 200) =>
            new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
        if (url.pathname === '/api/auth/session') return json({ authenticated: true, csrf_token: 'tok' });
        if (url.pathname === '/api/fs') {
            return json({
                path: url.searchParams.get('path'),
                entries: entriesFor[url.searchParams.get('path') || ''] || [],
            });
        }
        const body = init?.body ? JSON.parse(String(init.body)) : {};
        if (url.pathname === '/api/fs/write') {
            writes.push({ query: url.search, body });
            return json(writeBody, writeStatus);
        }
        if (url.pathname === '/api/fs/mkdir') {
            mkdirs.push({ body });
            return json({});
        }
        return json({});
    }));
});

afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
});

function openBrowser(onFileCreated = vi.fn()) {
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
            onFileCreated,
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
    await waitFor(() => expect(container.querySelectorAll('.file-browser-item').length).toBe(2));
}

/** Open the New file row, focus its field the way a finger does, and name the file. */
async function nameANewFile(container: HTMLElement, headerAria: string, name: string) {
    await fireEvent.click(container.querySelector(`button[aria-label="${headerAria}"]`) as HTMLElement);
    const input = container.querySelector<HTMLInputElement>('.new-file-input')!;
    input.focus();
    input.value = name;
    input.dispatchEvent(new Event('input', { bubbles: true }));
    await fireEvent.keyDown(input, { key: 'Enter' });
}

const errorText = (container: HTMLElement) =>
    [...container.querySelectorAll('.file-browser-error')].map(e => e.textContent).join('\n');

describe('a created file is opened, and created as a create', () => {
    it('hands the file it wrote to the screen, by the path it listed', async () => {
        const created = vi.fn();
        const { container } = openBrowser(created);
        await loadRoot(container);

        await nameANewFile(container, 'Create new file', 'notes.md');

        await waitFor(() => expect(created).toHaveBeenCalledWith(`${ROOT}/notes.md`));
        expect(writes[0].body).toEqual({ path: `${ROOT}/notes.md`, content: '', exclusive: true });
        expect(writes[0].query).toContain(`project=${PROJECT}`);
        expect(writes[0].query).toContain(`task=${TASK}`);
    });

    it('keeps the keyboard out of the listing, which held the name row it just closed', async () => {
        const { container } = openBrowser();
        await loadRoot(container);

        await nameANewFile(container, 'Create new file', 'notes.md');
        await waitFor(() => expect(container.querySelector('.new-file-input')).toBeNull());
        // The listing reloads and only then decides whether to take the keyboard back, so the claim is
        // checked after that decision had its chance — an assertion racing it proves nothing.
        await waitFor(() => expect(writes.length).toBe(1));
        await new Promise(r => setTimeout(r, 50));
        await tick();

        const list = container.querySelector<HTMLElement>('.file-browser-list')!;
        expect(document.activeElement).not.toBe(list);
    });

    it('opens no file for a folder, which has no buffer to type into', async () => {
        const created = vi.fn();
        const { container } = openBrowser(created);
        await loadRoot(container);

        await nameANewFile(container, 'Create new folder', 'notes');

        await waitFor(() => expect(mkdirs.length).toBe(1));
        expect(mkdirs[0].body).toEqual({ path: `${ROOT}/notes` });
        expect(created).not.toHaveBeenCalled();
        // A folder is opened into, and the listing keeps the keyboard it had.
        await waitFor(() => expect(container.querySelector('.file-browser-list')).toBe(document.activeElement));
    });
});

describe('a create the server refuses opens nothing', () => {
    it('says the reason, keeps the name for the person to change, and opens no tab', async () => {
        writeStatus = 400;
        writeBody = { error: 'path already exists' };
        const created = vi.fn();
        const { container } = openBrowser(created);
        await loadRoot(container);

        await nameANewFile(container, 'Create new file', 'a.txt');

        await waitFor(() => expect(errorText(container)).toContain('path already exists'));
        expect(created).not.toHaveBeenCalled();
        expect(container.querySelector<HTMLInputElement>('.new-file-input')?.value).toBe('a.txt');
    });
});
