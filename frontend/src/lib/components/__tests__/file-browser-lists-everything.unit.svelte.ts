import { describe, it, expect, vi, afterEach, beforeEach } from 'vitest';
import { render, cleanup, fireEvent, waitFor } from '@testing-library/svelte';
import FileBrowserView from '$lib/components/FileBrowserView.svelte';

/**
 * The listing draws every row the server sent and dims the ones git excludes. `.git` never arrives to be
 * dropped — the server unlists it (`handler_fs_gitdir_test.go`); the add-project picker hides names by
 * design in `file-browser-hidden-files.unit.svelte.ts`.
 */

const ROOT = '/home/sandbox/worktrees/fs-dots';

const LISTING: Record<string, { name: string; isDir: boolean; path: string; ignored?: boolean }[]> = {
    [ROOT]: [
        { name: '.github', isDir: true, path: `${ROOT}/.github` },
        { name: 'src', isDir: true, path: `${ROOT}/src` },
        { name: '.cache', isDir: true, path: `${ROOT}/.cache`, ignored: true },
        { name: 'node_modules', isDir: true, path: `${ROOT}/node_modules`, ignored: true },
        { name: '.env', isDir: false, path: `${ROOT}/.env`, ignored: true },
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
    await waitFor(() => expect(rowEls(container).length).toBe(6));
}

describe('the file listing draws what the server listed', () => {
    it('lists every row, and dims only the ones git excludes', async () => {
        const { container } = openBrowser();
        await loadRoot(container);

        const names = rowNames(container);
        for (const shown of ['.github', 'src', '.cache', 'node_modules', '.env', '.gitignore']) {
            expect(names).toContain(shown);
        }
        for (const name of ['.cache', 'node_modules', '.env']) {
            expect(rowEl(container, `${ROOT}/${name}`)!.classList.contains('ignored')).toBe(true);
        }
        for (const name of ['.github', 'src', '.gitignore']) {
            expect(rowEl(container, `${ROOT}/${name}`)!.classList.contains('ignored')).toBe(false);
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
