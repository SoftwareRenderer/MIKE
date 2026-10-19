// @vitest-environment jsdom
// A file that moved under an unsaved draft: the draft is merged into the file's newer bytes on the way in,
// and where the two cannot both stand the buffer carries both versions until they are resolved.
// docs/reconnection.md → "A draft carries the bytes it diverged from".
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, cleanup, act } from '@testing-library/svelte';

vi.mock('svelte', async (importOriginal) => {
    const svelte = await importOriginal<typeof import('svelte')>();
    return {
        ...svelte,
        onMount: (cb: () => void | Promise<void>) => { void Promise.resolve().then(cb); },
    };
});

const readFsFile = vi.fn();
const writeFsFile = vi.fn();
const showToast = vi.fn();
const showConfirm = vi.fn();

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
    dialogStore: {
        showToast: (...args: unknown[]) => showToast(...args),
        showConfirm: (...args: unknown[]) => showConfirm(...args),
    },
}));

// What this browser's IndexedDB holds: the buffer it left, and the file bytes it left it on.
const drafts: Record<string, { text: string; base: string | null }> = {};

vi.mock('$lib/storage', () => ({
    getEditorDraft: (path: string) => Promise.resolve(drafts[path] ?? null),
    setEditorDraft: (path: string, text: string, base: string | null) => {
        drafts[path] = { text, base };
        return Promise.resolve();
    },
    clearEditorDraft: (path: string) => { delete drafts[path]; return Promise.resolve(); },
    loadEditorHistory: () => Promise.resolve(null),
    saveEditorHistory: () => Promise.resolve(),
    loadAppSetting: () => Promise.resolve(false),
    saveAppSetting: () => Promise.resolve(),
}));

// No language server in this process; the editor must reach its document without one.
vi.mock('$lib/utils/lsp-connection-manager', () => ({
    isLSPAvailable: () => false,
    requestLSPChannel: () => ({ release: () => {} }),
}));

import FileEditor from '$lib/components/FileEditor.svelte';
import { CONFLICT_OURS, CONFLICT_SEPARATOR, CONFLICT_THEIRS } from '$lib/utils/editor-merge';

const FILE = '/repo/notes.txt';
const BASE = 'one\ntwo\nthree\n';

async function advance(ms: number) {
    await act(async () => { await vi.advanceTimersByTimeAsync(ms); });
}

const viewOf = (container: HTMLElement) => (container.querySelector('.cm-editor') as any)?.__view;
const doc = (container: HTMLElement) => viewOf(container).state.doc.toString();
const writes = () => writeFsFile.mock.calls.map(c => c[0] as { path: string; content: string });

/** The editor open on a file whose bytes on the server are `disk`. */
async function open(disk: string): Promise<HTMLElement> {
    readFsFile.mockResolvedValue({ content: disk });
    const { container } = render(FileEditor, {
        props: {
            projectId: 'p1', projectPath: '/repo', taskId: 't1',
            filePath: FILE, targetLine: 0, onClose: () => {},
        },
    });
    await advance(1000);
    expect(viewOf(container)).toBeTruthy();
    return container;
}

/** What the Explorer's Save button ends up doing: a request addressed to this editor's root. */
async function save(container: HTMLElement) {
    await act(async () => {
        window.dispatchEvent(new CustomEvent('editor-save-request', {
            detail: { scope: container.querySelector<HTMLElement>('.file-editor') },
        }));
    });
    await advance(0);
}

beforeEach(() => {
    vi.useFakeTimers();
    for (const k of Object.keys(drafts)) delete drafts[k];
    readFsFile.mockReset();
    writeFsFile.mockReset().mockResolvedValue(undefined);
    showToast.mockReset();
    showConfirm.mockReset().mockResolvedValue(true);
});

afterEach(() => {
    cleanup();
    vi.useRealTimers();
});

describe('a draft the file moved under', () => {
    it('paints the draft and the file together, and says nothing', async () => {
        drafts[FILE] = { text: 'one\nDRAFT\ntwo\nthree\n', base: BASE };

        const el = await open('one\ntwo\nthree\nSERVER\n');

        expect(doc(el)).toBe('one\nDRAFT\ntwo\nthree\nSERVER\n');
        expect(showToast).not.toHaveBeenCalled();
    });

    it('keeps the draft alone when no base was recorded for it', async () => {
        drafts[FILE] = { text: 'typed before bases were recorded', base: null };

        const el = await open('rewritten by someone else entirely');

        expect(doc(el)).toBe('typed before bases were recorded');
    });

    it('re-baselines the merged buffer, so the same change is not merged twice', async () => {
        drafts[FILE] = { text: 'one\nDRAFT\ntwo\nthree\n', base: BASE };
        await open('one\ntwo\nthree\nSERVER\n');

        // The record the next open reads: the merged buffer, on the bytes it now sits on.
        expect(drafts[FILE]).toEqual({ text: 'one\nDRAFT\ntwo\nthree\nSERVER\n', base: 'one\ntwo\nthree\nSERVER\n' });
    });

    it('writes the merge, not the draft the reader left', async () => {
        drafts[FILE] = { text: 'one\nDRAFT\ntwo\nthree\n', base: BASE };
        const el = await open('one\ntwo\nthree\nSERVER\n');

        await save(el);

        expect(writes()).toMatchObject([{ path: FILE, content: 'one\nDRAFT\ntwo\nthree\nSERVER\n' }]);
        expect(showConfirm).not.toHaveBeenCalled();
    });

    it('paints both versions of a line each side changed, and says so', async () => {
        drafts[FILE] = { text: 'one\nDRAFT\nthree\n', base: BASE };

        const el = await open('one\nSERVER\nthree\n');

        expect(doc(el)).toBe(`one\n${CONFLICT_OURS}\nDRAFT\n${CONFLICT_SEPARATOR}\nSERVER\n${CONFLICT_THEIRS}\nthree\n`);
        expect(showToast).toHaveBeenCalledTimes(1);
    });
});

describe('saving a buffer that still holds conflict markers', () => {
    async function openConflict() {
        drafts[FILE] = { text: 'one\nDRAFT\nthree\n', base: BASE };
        return open('one\nSERVER\nthree\n');
    }

    it('asks first, and writes nothing until the answer is yes', async () => {
        const el = await openConflict();
        showConfirm.mockResolvedValue(false);

        await save(el);

        expect(showConfirm).toHaveBeenCalledTimes(1);
        expect(writes()).toEqual([]);
    });

    it('writes the markers when told to', async () => {
        const el = await openConflict();

        await save(el);

        expect(showConfirm).toHaveBeenCalledTimes(1);
        expect(writes()).toHaveLength(1);
        expect(writes()[0].content).toContain(CONFLICT_OURS);
    });

    it('asks once per conflict: one dialog, however many requests arrive at it', async () => {
        const el = await openConflict();
        let answer: (ok: boolean) => void = () => {};
        showConfirm.mockReturnValue(new Promise<boolean>(resolve => { answer = resolve; }));

        await save(el);
        await save(el);
        expect(showConfirm).toHaveBeenCalledTimes(1);

        await act(async () => { answer(true); });
        await advance(0);
        expect(writes()).toHaveLength(1);
    });

    it('stops asking once the markers are gone', async () => {
        const el = await openConflict();
        await act(async () => {
            viewOf(el).dispatch({
                changes: { from: 0, to: viewOf(el).state.doc.length, insert: 'resolved by hand\n' },
            });
        });

        await save(el);

        expect(showConfirm).not.toHaveBeenCalled();
        expect(writes()).toMatchObject([{ path: FILE, content: 'resolved by hand\n' }]);
    });
});

describe('the record a buffer leaves behind', () => {
    /** The bytes the file holds. A test changes them the way anything else on this machine would. */
    let disk = BASE;

    /** The editor open on the file. Reads answer from `disk`, so a later read sees the same bytes. */
    async function openOnDisk() {
        const rendered = render(FileEditor, {
            props: {
                projectId: 'p1', projectPath: '/repo', taskId: 't1',
                filePath: FILE, targetLine: 0, onClose: () => {},
            },
        });
        await advance(1000);
        return rendered;
    }

    beforeEach(() => {
        disk = BASE;
        readFsFile.mockImplementation(() => Promise.resolve({ content: disk }));
    });

    /** An edit at the top of the buffer, then the wait that persists it. */
    async function typeAndSettle(el: HTMLElement, text: string) {
        await act(async () => {
            viewOf(el).dispatch({ changes: { from: 0, insert: text } });
        });
        await advance(400);
    }

    it('holds the buffer and the bytes it diverged from while they differ', async () => {
        const { container } = await openOnDisk();

        await typeAndSettle(container, 'typed ');

        expect(drafts[FILE]).toEqual({ text: `typed ${BASE}`, base: BASE });
    });

    it('goes back when the buffer is undone to what the file says', async () => {
        const { container } = await openOnDisk();
        await typeAndSettle(container, 'typed ');
        expect(drafts[FILE]).toBeTruthy();

        // Typing it away again leaves nothing for the next open.
        await act(async () => {
            viewOf(container).dispatch({ changes: { from: 0, to: 'typed '.length, insert: '' } });
        });
        await advance(400);

        expect(drafts[FILE]).toBeUndefined();
    });

    it('stays absent when a file change the buffer adopts reaches it by dispatch', async () => {
        const { container, rerender } = await openOnDisk();
        expect(drafts[FILE]).toBeUndefined();

        disk = 'one\ntwo\nthree\nSERVER\n';
        await rerender({ pendingFsEvent: { op: 'create', path: 'notes.txt' } });
        await advance(1000);

        // Nothing was unsaved, so the buffer took the file wholesale.
        expect(doc(container)).toBe('one\ntwo\nthree\nSERVER\n');
        // And the wait the dispatch arms must not record a buffer equal to its own file.
        expect(drafts[FILE]).toBeUndefined();
    });
});
