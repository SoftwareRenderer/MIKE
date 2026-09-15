import { describe, it, expect, vi, afterEach, beforeAll } from 'vitest';
import { render, cleanup, fireEvent, waitFor } from '@testing-library/svelte';
import TaskFileBrowser from '$lib/components/TaskFileBrowser.svelte';
import BranchFileBrowser from '$lib/components/BranchFileBrowser.svelte';

/**
 * A creation that the server refuses has to be said out loud. Both browsers write the failure to
 * state; whether it reaches the screen is a rendering question, and the task explorer had no
 * markup for it: the dialog's spinner cleared, the row stayed open showing the typed name, and the
 * user had to guess whether the folder existed. Pinned here for both hosts, because both render it
 * through the one surface in FileBrowserView — a fork that drops it again fails here.
 *
 * `onMount` is a no-op under vitest, so the listing never loads; that costs nothing, since the
 * create flow needs no listing to fail against.
 */

const PROJECT = 'proj-1';
const TASK = 'task-9';
const REFUSAL = 'path is outside the allowed host directories';
const PROJECT_PATH = '/srv/git/bear';

/** Every request the component made, so the scoping the create path lost is visible too. */
let calls: string[] = [];

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
    calls = [];
});

/** The create routes are refused; nothing else is asked for, since no listing loads here. */
function stubRefusingServer() {
    vi.stubGlobal('fetch', vi.fn(async (input: string) => {
        const url = new URL(String(input), 'http://localhost');
        calls.push(url.pathname + url.search);
        const json = (body: unknown, status = 200) =>
            new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });

        if (url.pathname === '/api/auth/session') return json({ authenticated: true, csrf_token: 'tok' });
        // The file create is refused with the server's reason; the folder create answers with
        // nothing but a status, which is the case the catalog message covers.
        if (url.pathname === '/api/fs/write') return json({ error: REFUSAL }, 403);
        if (url.pathname === '/api/fs/mkdir') return json({}, 500);
        return json({ error: 'unexpected request' }, 404);
    }));
}

const taskProps = {
    projectId: PROJECT,
    projectName: 'bear',
    taskId: TASK,
    taskPath: '/srv/worktrees/temp/bear-burnt-honey',
    sandboxEnabled: false,
    unsavedFiles: [] as string[],
    pendingFsEvent: null,
    onFileSelected: () => {},
    onPathChange: () => {},
};

const branchProps = {
    projectId: PROJECT,
    projectPath: PROJECT_PATH,
    sandboxEnabled: false,
    unsavedFiles: [] as string[],
    pendingFsEvent: null,
    onFileSelected: () => {},
    onPathChange: () => {},
};

/** Click a header button, name the new thing, press Enter — the whole create flow. */
async function create(container: HTMLElement, buttonLabel: string, name: string) {
    await fireEvent.click(container.querySelector(`button[aria-label="${buttonLabel}"]`) as HTMLElement);
    const input = container.querySelector('.new-file-input') as HTMLInputElement;
    input.value = name;
    input.dispatchEvent(new Event('input', { bubbles: true })); // Svelte's oninput
    await fireEvent.keyDown(input, { key: 'Enter' });
}

/** What the one error surface says. A thrown Error reaches the row through `String(e)`, so the
 *  reason arrives behind an `Error: ` prefix — the sentence the user reads today. */
const errorText = (container: HTMLElement) =>
    [...container.querySelectorAll('.file-browser-error')].map(e => e.textContent).join('\n');

describe('a refused create names its reason in both file browsers', () => {
    it('says the server\'s reason when the task explorer cannot create the file', async () => {
        stubRefusingServer();
        const { container } = render(TaskFileBrowser, { props: taskProps });
        await create(container, 'Create new file', 'notes.md');

        await waitFor(() => expect(errorText(container)).toContain(REFUSAL));
        // The worktree the listing belongs to is the worktree the write belongs to.
        expect(calls.find(c => c.startsWith('/api/fs/write'))).toContain(`project=${PROJECT}&task=${TASK}`);
    });

    it('falls back to its own sentence when a refused folder create says nothing', async () => {
        stubRefusingServer();
        const { container } = render(TaskFileBrowser, { props: taskProps });
        await create(container, 'Create new folder', 'src');

        await waitFor(() => expect(errorText(container)).toContain('Failed to create folder'));
        expect(calls.find(c => c.startsWith('/api/fs/mkdir'))).toContain(`task=${TASK}`);
    });

    it('says the server\'s reason when the project explorer cannot create the file', async () => {
        stubRefusingServer();
        const { container } = render(BranchFileBrowser, { props: branchProps });
        await create(container, 'Create new file', 'notes.md');

        await waitFor(() => expect(errorText(container)).toContain(REFUSAL));
        // A project's own checkout: the request carries the project and no task.
        const write = calls.find(c => c.startsWith('/api/fs/write')) || '';
        expect(write).toContain(`project=${PROJECT}`);
        expect(write).not.toContain('task=');
    });
});
