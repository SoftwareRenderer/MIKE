// @vitest-environment jsdom
// The name a diff panel's read carries. The read route takes a repository-relative name and takes the tree
// from `?task=`, so a path the row built out of the workspace directory has to be cut back to the name the
// repository gives it — an absolute one is refused before the jail is asked anything
// (`backend/server/handler_fs_blob.go`, `repoRelativePath`).
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

vi.mock('$lib/api', async (importOriginal) => {
    const real = await importOriginal<typeof import('$lib/api')>();
    return {
        ...real,
        readFsFile: (params: { path: string }) => readFsFile(params),
        writeFsFile: vi.fn(async () => undefined),
    };
});

vi.mock('$lib/stores', () => ({
    appStore: { config: {} },
    dialogStore: { showToast: vi.fn(), showConfirm: vi.fn(async () => true) },
}));

vi.mock('$lib/storage', () => ({
    getEditorDraft: () => Promise.resolve(null),
    setEditorDraft: () => Promise.resolve(),
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

const WORKSPACE = '/workspaces';

/** Every name the reads asked for, in the order they were asked. */
const readNames = () => readFsFile.mock.calls.map(call => (call[0] as { path: string }).path);

/** The editor open on `path`, with the two reads it makes (the file, then its baseline) settled. */
async function open(path: string, props: Record<string, unknown> = {}): Promise<void> {
    readFsFile.mockResolvedValue({ content: 'alpha', gutter_markers: {} });
    render(FileEditor, {
        props: {
            projectId: 'p1',
            taskId: 't1',
            filePath: path,
            targetLine: 0,
            onClose: () => {},
            ...props,
        },
    });
    await act(async () => { await vi.advanceTimersByTimeAsync(1000); });
}

beforeEach(() => {
    vi.useFakeTimers();
    readFsFile.mockReset();
});

afterEach(() => {
    cleanup();
    vi.useRealTimers();
});

describe('the name a read carries', () => {
    it('names a host task\'s file by the worktree the row was built from', async () => {
        // The task card's diff panel opens a clicked row under the workspace, while the project row names a
        // checkout elsewhere — so only the task's worktree root cuts this to a name the route accepts.
        await open(`${WORKSPACE}/p1/t1/src/a.go`, {
            mode: 'diff',
            workspaceDir: WORKSPACE,
            projectPath: '/home/gabba/projects/watchfaces',
        });

        expect(readNames()).toEqual(['src/a.go', 'src/a.go']);
    });

    it('names a sandboxed task\'s file by the container root that lists it', async () => {
        await open('/home/sandbox/watchfaces/preview.html', {
            mode: 'diff',
            workspaceDir: WORKSPACE,
            projectPath: '/home/gabba/projects/watchfaces',
            containerPath: '/home/sandbox/watchfaces',
        });

        expect(readNames()).toEqual(['preview.html', 'preview.html']);
    });

    it('names an explorer file by the checkout its tree was listed from', async () => {
        await open('/repo/src/b.go', { projectPath: '/repo' });

        expect(readNames()).toEqual(['src/b.go']);
    });
});
