// @vitest-environment jsdom
// The editor's half of the binary Preview: the notice states the file's size and wears a Preview button
// only while an installed extension declares it renders that file type. The frame it opens is mounted
// by the host and names the file in its own context — the frame's URL carries the channel token and
// nothing else, because a file an extension could name in a URL is a file it could choose.
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
const statFsPaths = vi.fn();

vi.mock('$lib/api', async (importOriginal) => {
    const real = await importOriginal<typeof import('$lib/api')>();
    return {
        ...real,
        readFsFile: (params: { path: string }) => readFsFile(params),
        writeFsFile: vi.fn(async () => undefined),
        statFsPaths: (params: { paths: string[] }) => statFsPaths(params),
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
import { FsReadRefused } from '$lib/api';
import { uiInjector } from '$lib/services/ui-injector.svelte';
import type { Extension } from '$lib/types';

const PROJECT = '/repo';

function viewerExtension(suffixes: string[]): Extension {
    return {
        id: 'ext-viewer',
        name: 'mesh-viewer',
        version: '1.0.0',
        type: 'ui',
        enabled: true,
        manifest: {
            display_name: 'Mesh Viewer',
            description: 'Test',
            author: 'Test',
            version: '1.0.0',
            capabilities: {
                ui: { injection_points: ['binaryPreview'], file_suffixes: suffixes, permissions: ['fs.getBytes'] },
            },
        },
        config: {},
        container_config: {},
        sandbox_config: { network_isolation: false, memory_limit: 0 },
        running: false,
        installed_at: '',
        built_at: '',
        install_source: 'local',
    } as Extension;
}

async function advance(ms: number) {
    await act(async () => { await vi.advanceTimersByTimeAsync(ms); });
}

/** The editor open on `path` after the read refused it as text. */
async function openRefused(path: string): Promise<HTMLElement> {
    readFsFile.mockRejectedValue(new FsReadRefused(415, 'this file is not text'));
    const { container } = render(FileEditor, {
        props: { projectId: 'p1', projectPath: PROJECT, taskId: 't1', filePath: path, targetLine: 0, onClose: () => {} },
    });
    await advance(1000);
    return container;
}

const previewButton = (container: HTMLElement) => container.querySelector<HTMLElement>('.editor-preview-binary');

beforeEach(() => {
    vi.useFakeTimers();
    readFsFile.mockReset();
    statFsPaths.mockReset().mockResolvedValue([{ name: 'gear.zbin', path: 'mesh/gear.zbin', kind: 'file', bytes: 8192 }]);
});

afterEach(() => {
    cleanup();
    vi.useRealTimers();
    uiInjector.registerFromExtensions([]);
});

describe('a file whose bytes the editor will not hold', () => {
    it('states the size and offers Preview for a type an extension declared', async () => {
        uiInjector.registerFromExtensions([viewerExtension(['zbin'])]);
        const el = await openRefused(`${PROJECT}/mesh/gear.zbin`);

        expect(el.textContent).toContain('8.0 KB');
        const button = previewButton(el);
        expect(button, 'no Preview button on a declared file type').toBeTruthy();

        await fireEvent.click(button!);
        await advance(50);
        const dialog = el.querySelector('[role="dialog"]');
        expect(dialog, 'the preview modal never opened').toBeTruthy();
        expect(dialog!.textContent).toContain('mesh/gear.zbin');
    });

    it('mounts its frame with the file in the host context, never in the frame URL', async () => {
        uiInjector.registerFromExtensions([viewerExtension(['zbin'])]);
        const el = await openRefused(`${PROJECT}/mesh/gear.zbin`);
        await fireEvent.click(previewButton(el)!);
        await advance(50);

        const frame = el.querySelector('iframe')!;
        expect(frame, 'the extension frame was never mounted').toBeTruthy();
        const src = new URL(frame.getAttribute('src')!, 'http://x');
        expect(src.pathname).toBe('/extensions/test/mesh-viewer/');
        expect(src.searchParams.get('extch')).toBeTruthy();
        expect(src.searchParams.get('path'), 'a frame that could name a file in its URL could choose one').toBeNull();
    });

    it('offers nothing for a type nobody declared, and says no size-less notice', async () => {
        uiInjector.registerFromExtensions([viewerExtension(['zbin'])]);
        const el = await openRefused(`${PROJECT}/vendor/blob.bin`);

        expect(previewButton(el)).toBeNull();
        // The notice is still the right answer, with the size the stat measured.
        expect(el.textContent).toContain('Binary file');
        expect(el.textContent).toContain('8.0 KB');
    });

    it('offers nothing when no extension is installed', async () => {
        uiInjector.registerFromExtensions([]);
        const el = await openRefused(`${PROJECT}/mesh/gear.zbin`);

        expect(previewButton(el)).toBeNull();
    });

    it('never asks the server how big a file it can show as text is', async () => {
        uiInjector.registerFromExtensions([viewerExtension(['zbin'])]);
        readFsFile.mockResolvedValue({ content: 'plain text', size: 10 });
        const { container } = render(FileEditor, {
            props: { projectId: 'p1', projectPath: PROJECT, taskId: 't1', filePath: `${PROJECT}/notes.md`, targetLine: 0, onClose: () => {} },
        });
        await advance(1000);

        expect(statFsPaths).not.toHaveBeenCalled();
        expect(previewButton(container)).toBeNull();
    });
});
