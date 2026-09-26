// @vitest-environment jsdom
// A save that got no answer keeps the screen as it was, says nothing, and writes the buffer again — once.
// docs/reconnection.md → "A file save the connection lost waits with the editor, not in the queue".
//
// Vitest no-ops `onMount` and never runs `onDestroy`, so this file installs the save listener itself, and
// each test opens its own file and addresses its save to its own editor root: an editor an earlier test
// rendered is still alive and still listening.
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

// Only the file requests are stubbed: the editor branches on `isTransientError`, so that and
// `ConnectionFailure` stay real.
vi.mock('$lib/api', async (importOriginal) => {
    const real = await importOriginal<typeof import('$lib/api')>();
    return {
        ...real,
        readFsFile: (params: { path: string }) => readFsFile(params),
        writeFsFile: (params: { path: string }) => writeFsFile(params),
    };
});

const showToast = vi.fn();
vi.mock('$lib/stores', () => ({
    appStore: { config: {} },
    dialogStore: { showToast: (...args: unknown[]) => showToast(...args) },
}));

// Every buffer is dirty the way a real one is after a reload: the browser's own draft differs from the file.
const DRAFT = 'edited by hand';
const DISK = 'on disk';
vi.mock('$lib/storage', () => ({
    getEditorDraft: () => Promise.resolve(DRAFT),
    setEditorDraft: () => Promise.resolve(),
    clearEditorDraft: () => Promise.resolve(),
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
import { ConnectionFailure } from '$lib/api/helpers';
import { SAVE_RETRY_BASE_DELAY_MS, SAVE_RETRY_MAX_DELAY_MS } from '$lib/constants';
import { connectionStore } from '$lib/stores/connection.svelte';

type Write = { path: string; content: string };
const writes = () => writeFsFile.mock.calls.map((c) => c[0] as Write);
/** Only this test's writes, which an editor left over from an earlier test cannot inflate. */
const writesTo = (path: string) => writes().filter((w) => w.path === path);

function open(path: string): { container: HTMLElement; save: () => Promise<void> } {
    const { container } = render(FileEditor, {
        props: {
            projectId: 'p1',
            projectPath: '/repo',
            taskId: 't1',
            filePath: path,
            targetLine: 0,
            onClose: () => {},
        },
    });
    const root = container.querySelector<HTMLElement>('.file-editor');
    return {
        container,
        // The scoped save request: the tab bar's is unscoped and would reach every live editor.
        save: async () => {
            await act(async () => {
                window.dispatchEvent(new CustomEvent('editor-save-request', { detail: { scope: root } }));
            });
        },
    };
}

/** The document area, and the pane a file that cannot be read shows in its place. */
const document_ = (container: HTMLElement) => container.querySelector('.cm-content');
const errorPane = (container: HTMLElement) => container.querySelector('.editor-error');

/** Fake clocks, so a wait one test arms cannot fire inside the next. */
async function advance(ms: number) {
    await act(async () => {
        await vi.advanceTimersByTimeAsync(ms);
    });
}

let nextPath = 0;
async function openEditor(): Promise<{ path: string; container: HTMLElement; save: () => Promise<void> }> {
    const path = `/repo/file${nextPath++}.go`;
    const editor = open(path);
    await advance(100); // the load, then the frame in which the editor is built
    expect(document_(editor.container)).toBeTruthy();
    return { path, ...editor };
}

beforeEach(() => {
    vi.useFakeTimers();
    connectionStore.set(false);
    readFsFile.mockReset().mockResolvedValue({ content: DISK });
    writeFsFile.mockReset().mockResolvedValue(undefined);
    showToast.mockReset();
});

afterEach(() => {
    cleanup();
    vi.useRealTimers();
});

describe('a save that got no answer', () => {
    it('keeps the document on screen instead of replacing it with the failure', async () => {
        writeFsFile.mockRejectedValue(new ConnectionFailure());
        const { path, container, save } = await openEditor();

        await save();

        expect(writesTo(path)).toMatchObject([{ path, content: DRAFT }]);
        // This is the pane a file that cannot be read shows; a failed save must never show it.
        expect(errorPane(container)).toBeNull();
        expect(document_(container)).toBeTruthy();
    });

    it('says nothing, and frees the explorer’s Save button as unsettled', async () => {
        writeFsFile.mockRejectedValue(new ConnectionFailure());
        const settled: Array<{ path?: string; ok?: boolean }> = [];
        const onResponse = (e: Event) => settled.push((e as CustomEvent).detail);
        window.addEventListener('editor-save-settled', onResponse);
        const { path, save } = await openEditor();

        await save();
        window.removeEventListener('editor-save-settled', onResponse);

        expect(showToast).not.toHaveBeenCalled();
        expect(settled).toEqual([{ path, ok: false }]);
    });

    it('writes the buffer again on a growing wait — one write per rung, never two', async () => {
        const attemptedAt: number[] = [];
        writeFsFile.mockImplementation(() => {
            attemptedAt.push(Date.now());
            return Promise.reject(new ConnectionFailure());
        });
        const { path, save } = await openEditor();

        await save();
        expect(writesTo(path)).toHaveLength(1);

        // Nothing goes out before the first wait is over.
        await advance(SAVE_RETRY_BASE_DELAY_MS - 1);
        expect(writesTo(path)).toHaveLength(1);

        await advance(SAVE_RETRY_BASE_DELAY_MS * 20);
        // Gaps double from the base, then hold at the ceiling; a 0 here would mean one rung fired twice.
        const gaps = attemptedAt.slice(1).map((t, i) => t - attemptedAt[i]);
        expect(gaps.slice(0, 4)).toEqual([
            SAVE_RETRY_BASE_DELAY_MS,
            SAVE_RETRY_BASE_DELAY_MS * 2,
            SAVE_RETRY_BASE_DELAY_MS * 4,
            SAVE_RETRY_MAX_DELAY_MS,
        ]);
    });

    it('sends the buffer it holds when it retries, not the one that failed', async () => {
        writeFsFile.mockRejectedValue(new ConnectionFailure());
        const { path, save } = await openEditor();

        await save();
        expect(writesTo(path)[0].content).toBe(DRAFT);

        await advance(SAVE_RETRY_BASE_DELAY_MS);
        expect(writesTo(path)[1].content).toBe(DRAFT);
    });

    it('stops waiting once the write lands', async () => {
        writeFsFile.mockRejectedValueOnce(new ConnectionFailure());
        const { path, container, save } = await openEditor();

        await save();
        expect(writesTo(path)).toHaveLength(1);

        await advance(SAVE_RETRY_BASE_DELAY_MS);
        expect(writesTo(path)).toHaveLength(2);

        // The buffer is on disk now, so there is nothing left to write.
        await advance(SAVE_RETRY_MAX_DELAY_MS * 2);
        expect(writesTo(path)).toHaveLength(2);
        expect(errorPane(container)).toBeNull();
        expect(document_(container)).toBeTruthy();
    });

    it('is written again when the socket comes back, without waiting out the delay', async () => {
        writeFsFile.mockRejectedValue(new ConnectionFailure());
        const { path, save } = await openEditor();

        await save();
        expect(writesTo(path)).toHaveLength(1);

        await act(async () => {
            connectionStore.set(true);
        });
        expect(writesTo(path)).toHaveLength(2);
    });
});

describe('a save the server refused', () => {
    it('is said once, in the server’s own sentence, and is never asked again', async () => {
        writeFsFile.mockRejectedValue(new Error('path is outside the project'));
        const { path, container, save } = await openEditor();

        await save();

        expect(showToast).toHaveBeenCalledWith('path is outside the project', 'error');
        expect(errorPane(container)).toBeNull();
        expect(document_(container)).toBeTruthy();

        // A refusal is an answer; a later attempt asks the same question.
        await advance(SAVE_RETRY_MAX_DELAY_MS * 3);
        expect(writesTo(path)).toHaveLength(1);
    });

    it('says its own sentence when the refusal carries none', async () => {
        writeFsFile.mockRejectedValue(new Error(''));
        const { save } = await openEditor();

        await save();

        expect(showToast).toHaveBeenCalledWith('Failed to save', 'error');
    });
});
