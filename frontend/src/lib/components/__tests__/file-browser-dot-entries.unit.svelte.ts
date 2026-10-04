import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest';
import { render, cleanup, fireEvent, waitFor } from '@testing-library/svelte';
import FileBrowserView from '$lib/components/FileBrowserView.svelte';

/**
 * Both explorer listings show dot-prefixed names, and only the ignored directories stay hidden. The
 * add-project picker hides them behind its own option: `file-browser-hidden-files.unit.svelte.ts`.
 */

const ROOT = '/home/sandbox/worktrees/fs-dots';

const LISTING: Record<string, { name: string; isDir: boolean; path: string }[]> = {
    [ROOT]: [
        { name: '.github', isDir: true, path: `${ROOT}/.github` },
        { name: 'src', isDir: true, path: `${ROOT}/src` },
        { name: '.git', isDir: true, path: `${ROOT}/.git` },
        { name: '.cache', isDir: true, path: `${ROOT}/.cache` },
        { name: 'node_modules', isDir: true, path: `${ROOT}/node_modules` },
        { name: '.env', isDir: false, path: `${ROOT}/.env` },
        { name: '.gitignore', isDir: false, path: `${ROOT}/.gitignore` },
    ],
    [`${ROOT}/.github`]: [
        { name: 'workflows', isDir: true, path: `${ROOT}/.github/workflows` },
    ],
};

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
            const path = url.searchParams.get('path') || '';
            return json({ path, entries: LISTING[path] || [] });
        }
        return json({ error: 'unexpected request' }, 404);
    }));
});

afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
});

function openBrowser() {
    return render(FileBrowserView, {
        props: {
            projectId: 'proj-dots',
            taskId: 'task-dots',
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

const rowEls = (container: HTMLElement) =>
    [...container.querySelectorAll<HTMLElement>('.file-browser-item')];
const rowEl = (container: HTMLElement, path: string) =>
    container.querySelector<HTMLElement>(`.file-browser-item[title="${path}"]`);
const rowNames = (container: HTMLElement) =>
    rowEls(container).map(el => el.querySelector('.file-name')?.textContent ?? '');

// Load the root listing. onMount is stubbed in the vitest setup, and a typed path lands on the root.
async function loadRoot(container: HTMLElement) {
    const input = container.querySelector('.file-browser-path-input') as HTMLInputElement;
    input.value = ROOT;
    input.dispatchEvent(new Event('input', { bubbles: true }));
    await fireEvent.keyDown(input, { key: 'Enter' });
    await waitFor(() => expect(rowEls(container).length).toBe(4));
}

describe('the file listing\'s dot-prefixed rows', () => {
    it('lists dot files and a dot directory, and the ignored directories stay out', async () => {
        const { container } = openBrowser();
        await loadRoot(container);

        const names = rowNames(container);
        for (const shown of ['.github', 'src', '.env', '.gitignore']) {
            expect(names).toContain(shown);
        }
        for (const hidden of ['.git', '.cache', 'node_modules']) {
            expect(names).not.toContain(hidden);
        }
    });

    it('walks into the dot directory it lists', async () => {
        const { container } = openBrowser();
        await loadRoot(container);

        await fireEvent.click(rowEl(container, `${ROOT}/.github`)!);

        // The nested listing carries the `..` row the root one has no need of.
        await waitFor(() => expect(rowEls(container).length).toBe(2));
        expect(rowNames(container)).toEqual(['..', 'workflows']);
    });
});
