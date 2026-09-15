import { describe, it, expect, vi, afterEach, beforeAll } from 'vitest';
import { render, cleanup, waitFor } from '@testing-library/svelte';
import BranchFileBrowser from '$lib/components/BranchFileBrowser.svelte';

/**
 * The git status letter is the one thing the project explorer adds to a row, and it lives in the
 * host: the listing asks the host about each entry as it draws it. That holds only while the host's
 * own state is what the listing reads — copy the map into the listing, or read it outside the
 * render, and the glyph freezes at whatever it was when the folder was opened. This case delivers
 * the letter *after* the listing exists, which is when a frozen glyph shows.
 */

const PROJECT = 'proj-1';
const PROJECT_PATH = '/srv/git/bear';
const FILE = 'notes.md';

beforeAll(() => {
    vi.stubGlobal('matchMedia', (query: string) => ({
        matches: false, media: query, onchange: null,
        addEventListener: () => {}, removeEventListener: () => {},
        addListener: () => {}, removeListener: () => {},
        dispatchEvent: () => false,
    }));
});

afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
});

describe('the project explorer colors its rows from its own git map', () => {
    it('shows the letter for a row whose status arrives after the listing was drawn', async () => {
        // The watcher event is what pulls both: the status, and the listing it belongs to.
        vi.stubGlobal('fetch', vi.fn(async (input: string) => {
            const url = new URL(String(input), 'http://localhost');
            const json = (body: unknown, status = 200) =>
                new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

            if (url.pathname === '/api/auth/session') return json({ authenticated: true, csrf_token: 'tok' });
            if (url.pathname.endsWith('/git/status')) {
                return json({ clean: false, status: ` M ${FILE}`, head: 'abc', authorName: '', authorEmail: '' });
            }
            if (url.pathname === '/api/fs') {
                return json({ entries: [{ name: FILE, isDir: false, path: `${PROJECT_PATH}/${FILE}` }] });
            }
            return json({ error: 'unexpected request' }, 404);
        }));

        const { container } = render(BranchFileBrowser, {
            props: {
                projectId: PROJECT,
                projectPath: PROJECT_PATH,
                sandboxEnabled: false,
                unsavedFiles: [] as string[],
                pendingFsEvent: { op: 'delete' as const, path: FILE },
                onFileSelected: () => {},
                onPathChange: () => {},
            },
        });

        await waitFor(() => expect(container.querySelector('.file-browser-item')).toBeTruthy());
        const badge = await waitFor(() => {
            const el = container.querySelector('.git-status-badge');
            expect(el?.textContent).toBe('M');
            return el as HTMLElement;
        });
        expect(badge.className).toContain('git-status-modified');
        expect(badge.getAttribute('aria-label')).toBe('Git status: Modified');
    });
});
