// @vitest-environment jsdom
// Which bytes the editor starts from, and which file a throttled write lands on. The diff editor (`CardDetail`)
// reuses one instance for every file it shows, and a reopened file can hold a draft, an undo chain, or both.
// docs/reconnection.md → "A save the connection lost waits with the editor, not in the queue".
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, cleanup, act } from '@testing-library/svelte';
import { undoDepth } from '@codemirror/commands';

vi.mock('svelte', async (importOriginal) => {
    const svelte = await importOriginal<typeof import('svelte')>();
    return {
        ...svelte,
        onMount: (cb: () => void | Promise<void>) => { void Promise.resolve().then(cb); },
    };
});

const readFsFile = vi.fn();
const writeFsFile = vi.fn();

vi.mock('$lib/api', async (importOriginal) => {
    const real = await importOriginal<typeof import('$lib/api')>();
    return {
        ...real,
        readFsFile: (params: { path: string }) => readFsFile(params),
        writeFsFile: (params: { path: string }) => writeFsFile(params),
    };
});

vi.mock('$lib/stores', () => ({
    appStore: { config: {} },
    dialogStore: { showToast: () => {} },
}));

// What the browser's mirror holds, per path, as the real storage module would.
const drafts: Record<string, { text: string; base: string | null; ts: number }> = {};
const histories: Record<string, { ts: number; base: string; deltas: unknown[] }> = {};
const draftWrites: Array<{ path: string; text: string; base: string | null }> = [];
const historyWrites: Array<{ path: string }> = [];

vi.mock('$lib/storage', () => ({
    getEditorDraft: (path: string) => Promise.resolve(drafts[path] ?? null),
    setEditorDraft: (path: string, text: string, base: string | null) => {
        draftWrites.push({ path, text, base });
        drafts[path] = { text, base, ts: 1 };
        return Promise.resolve();
    },
    clearEditorDraft: (path: string) => { delete drafts[path]; return Promise.resolve(); },
    loadEditorHistory: (path: string) => Promise.resolve(histories[path] ?? null),
    saveEditorHistory: (path: string) => {
        historyWrites.push({ path });
        return Promise.resolve();
    },
    loadAppSetting: () => Promise.resolve(false),
    saveAppSetting: () => Promise.resolve(),
}));

// No language server in this process; the editor must reach its document without one.
vi.mock('$lib/utils/lsp-connection-manager', () => ({
    isLSPAvailable: () => false,
    requestLSPChannel: () => ({ release: () => {} }),
}));

import FileEditor from '$lib/components/FileEditor.svelte';

const A = '/repo/first.go';
const B = '/repo/second.go';
const DISK: Record<string, string> = { [A]: 'first on disk\n', [B]: 'second on disk\n' };

async function advance(ms: number) {
    await act(async () => {
        await vi.advanceTimersByTimeAsync(ms);
    });
}

const viewOf = (container: HTMLElement) =>
    (container.querySelector('.cm-editor') as any)?.__view;

/** The scoped save request — what the tab bar's Save button ends up doing. */
async function save(container: HTMLElement) {
    await act(async () => {
        window.dispatchEvent(new CustomEvent('editor-save-request', {
            detail: { scope: container.querySelector<HTMLElement>('.file-editor') },
        }));
    });
}

async function type(container: HTMLElement, text: string) {
    await act(async () => {
        viewOf(container).dispatch({ changes: { from: 0, to: 0, insert: text } });
    });
}

const writes = () => writeFsFile.mock.calls.map(c => c[0] as { path: string; content: string });

/** The editor open on file A, its document read from disk. */
async function openFirst() {
    const rendered = render(FileEditor, {
        props: {
            projectId: 'p1', projectPath: '/repo', taskId: 't1',
            filePath: A, targetLine: 0, onClose: () => {},
        },
    });
    await advance(1000);
    expect(viewOf(rendered.container)).toBeTruthy();
    return rendered;
}

beforeEach(() => {
    vi.useFakeTimers();
    for (const k of Object.keys(drafts)) delete drafts[k];
    for (const k of Object.keys(histories)) delete histories[k];
    draftWrites.length = 0;
    historyWrites.length = 0;
    // Every read is slower than both debounces, which is the window a pending wait can fall into.
    readFsFile.mockReset().mockImplementation(({ path }: { path: string }) =>
        new Promise(resolve => setTimeout(() => resolve({ content: DISK[path] ?? '' }), 800)));
    writeFsFile.mockReset().mockResolvedValue(undefined);
});

afterEach(() => {
    cleanup();
    vi.useRealTimers();
});

describe('a file switch inside the editor’s persistence debounce', () => {
    it('writes neither draft nor history for the file that arrives', async () => {
        const { container, rerender } = await openFirst();

        await type(container, 'typed ');
        // Nothing persisted yet: both waits are still outstanding.
        expect(draftWrites).toHaveLength(0);

        await rerender({ filePath: B });
        await advance(1500);

        expect(draftWrites.filter(w => w.path === B)).toEqual([]);
        expect(historyWrites.filter(w => w.path === B)).toEqual([]);
        // The first file's edit is still persisted, to the first file.
        expect(draftWrites.filter(w => w.path === A).at(-1)?.text).toBe('typed first on disk\n');
    });

    it('leaves the file that arrives with nothing it did not write', async () => {
        const { container, rerender } = await openFirst();
        await type(container, 'typed ');

        await rerender({ filePath: B });
        await advance(1500);

        // The buffer is what was read, so there is nothing to write. Before the wait carried its own file, this
        // was the empty body the switch left behind — and Save wrote it over the file.
        expect(viewOf(container).state.doc.toString()).toBe(DISK[B]);
        await save(container);
        expect(writes()).toEqual([]);

        // And an edit made here is saved as the text on screen.
        await type(container, 'x');
        await save(container);
        expect(writes()).toMatchObject([{ path: B, content: 'x' + DISK[B] }]);
    });
});

describe('a reload taken while a persistence wait is still open', () => {
    it('hands the buffer to storage on the way out', async () => {
        const { container } = await openFirst();
        await type(container, 'typed ');
        expect(draftWrites).toHaveLength(0);

        // A reload never destroys the component, so only the unload path can still save this buffer.
        await act(async () => { window.dispatchEvent(new Event('beforeunload')); });

        expect(draftWrites.filter(w => w.path === A).at(-1)?.text).toBe('typed first on disk\n');
    });
});

describe('a reopen whose draft and undo chain disagree', () => {
    it('paints and saves the draft, which is the buffer the reader left', async () => {
        drafts[A] = { text: 'typed and never saved', base: DISK[A], ts: 1 };
        histories[A] = {
            ts: Date.now(),
            base: DISK[A],
            // A chain that reaches somewhere else: it was written 300 ms before the draft above.
            deltas: [{ from: 0, to: 0, insert: 'older attempt' }],
        };

        const { container } = await openFirst();

        expect(viewOf(container).state.doc.toString()).toBe('typed and never saved');
        await save(container);
        expect(writes()).toMatchObject([{ path: A, content: 'typed and never saved' }]);
    });

    it('keeps undo when the chain reaches the buffer the draft holds', async () => {
        drafts[A] = { text: 'typed and never saved', base: DISK[A], ts: 1 };
        histories[A] = {
            ts: Date.now(),
            base: DISK[A],
            // One change that reaches the draft exactly, so the chain describes the buffer on screen.
            deltas: [{ from: 0, to: DISK[A].length, insert: 'typed and never saved' }],
        };

        const { container } = await openFirst();

        expect(viewOf(container).state.doc.toString()).toBe('typed and never saved');
        expect(undoDepth(viewOf(container).state)).toBeGreaterThan(0);
    });
});
