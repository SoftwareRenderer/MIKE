// @vitest-environment jsdom
// What the editor does with bytes refused as text: a picture painted from the raw route, or the sentence that
// the file is binary; neither has a buffer, a dirty flag or a save. The refusal itself is
// `backend/server/handler_fs_read_text_test.go`.
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { render, cleanup, act, fireEvent } from '@testing-library/svelte';

vi.mock('svelte', async (importOriginal) => {
    const svelte = await importOriginal<typeof import('svelte')>();
    return {
        ...svelte,
        onMount: (cb: () => void | Promise<void>) => { void Promise.resolve().then(cb); },
    };
});

const readFsFile = vi.fn();
const writeFsFile = vi.fn();
const draftsWritten: string[] = [];

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
    dialogStore: { showToast: vi.fn(), showConfirm: vi.fn(async () => true) },
}));

vi.mock('$lib/storage', () => ({
    getEditorDraft: () => Promise.resolve(null),
    setEditorDraft: (path: string) => { draftsWritten.push(path); return Promise.resolve(); },
    clearEditorDraft: () => Promise.resolve(),
    loadEditorHistory: () => Promise.resolve(null),
    saveEditorHistory: () => Promise.resolve(),
    loadAppSetting: () => Promise.resolve(false),
    saveAppSetting: () => Promise.resolve(),
}));

vi.mock('$lib/utils/lsp-connection-manager', () => ({
    isLSPAvailable: () => false,
    requestLSPChannel: () => ({ release: () => {} }),
}));

import FileEditor from '$lib/components/FileEditor.svelte';
import { FsReadRefused } from '$lib/api';

const PROJECT = '/repo';

async function advance(ms: number) {
    await act(async () => { await vi.advanceTimersByTimeAsync(ms); });
}

/** The editor open on `path` after the read refused it as text. */
async function openRefused(path: string, props: Record<string, unknown> = {}): Promise<HTMLElement> {
    readFsFile.mockRejectedValue(new FsReadRefused(415, 'this file is not text'));
    const { container } = render(FileEditor, {
        props: { projectId: 'p1', projectPath: PROJECT, taskId: 't1', filePath: path, targetLine: 0, onClose: () => {}, ...props },
    });
    await advance(1000);
    return container;
}

const picture = (container: HTMLElement) => container.querySelector<HTMLImageElement>('img.editor-image-img');

beforeEach(() => {
    vi.useFakeTimers();
    readFsFile.mockReset();
    writeFsFile.mockReset().mockResolvedValue(undefined);
    draftsWritten.length = 0;
});

afterEach(() => {
    cleanup();
    vi.useRealTimers();
});

describe('a file whose bytes are not text', () => {
    it('paints a picture the browser can render from the raw route', async () => {
        const el = await openRefused(`${PROJECT}/docs/logo.png`);

        const img = picture(el)!;
        expect(img).toBeTruthy();
        const url = new URL(img.getAttribute('src')!, 'http://x');
        expect(url.pathname).toBe('/api/fs/raw');
        expect(url.searchParams.get('project')).toBe('p1');
        expect(url.searchParams.get('task')).toBe('t1');
        expect(url.searchParams.get('path')).toBe('docs/logo.png');
    });

    it('asks for the picture of a sandboxed tree by the name its container lists it under', async () => {
        // A sandboxed task's listing opens files under the project's own mount while projectPath is the
        // worktree directory, so only the container root strips to a path the raw route accepts.
        const el = await openRefused('/home/sandbox/immich-watchface/preview-tie.png', {
            projectPath: '/repo/.worktrees/brave-fox',
            containerPath: '/home/sandbox/immich-watchface',
        });

        const url = new URL(picture(el)!.getAttribute('src')!, 'http://x');
        expect(url.pathname).toBe('/api/fs/raw');
        expect(url.searchParams.get('task')).toBe('t1');
        expect(url.searchParams.get('path')).toBe('preview-tie.png');
    });

    // The task card's diff panel opens a clicked file through the same states, under the same mount — and a
    // folder not named like the project, so no guess at the mount can match.
    it('paints that picture in the diff viewer too', async () => {
        const el = await openRefused('/home/sandbox/immich-watchface/preview-tie.png', {
            mode: 'diff',
            projectPath: '/repo/watchfaces',
            containerPath: '/home/sandbox/immich-watchface',
        });

        expect(el.querySelector('.editor-header')).toBeTruthy();   // the diff pane, not the tab's
        const url = new URL(picture(el)!.getAttribute('src')!, 'http://x');
        expect(url.searchParams.get('path')).toBe('preview-tie.png');
    });

    it('holds no buffer for it, so nothing can be saved back', async () => {
        const el = await openRefused(`${PROJECT}/assets/data.zip`);

        expect(containerText(el)).toContain('Binary file');
        expect(picture(el)).toBeNull();
        expect(el.querySelector('.cm-editor')).toBeNull();
        expect(el.querySelector('.editor-placeholder')?.textContent).toContain(`${PROJECT}/assets/data.zip`);
        expect(draftsWritten).toEqual([]);
        expect(writeFsFile).not.toHaveBeenCalled();
    });

    it('says the picture did not arrive instead of hanging on a blank pane', async () => {
        const el = await openRefused(`${PROJECT}/docs/logo.png`);
        await fireEvent.error(picture(el)!);

        expect(picture(el)).toBeNull();
        expect(containerText(el)).toContain('could not be loaded');
    });
});

function containerText(container: HTMLElement) {
    return container.querySelector('.editor-content')?.textContent ?? '';
}
