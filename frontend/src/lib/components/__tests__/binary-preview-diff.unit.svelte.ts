// @vitest-environment jsdom
// The Preview a binary diff row wears, and the file its frame is mounted on. An image row already has
// "View image"; this is the same affordance for a format the browser cannot paint, and it exists only
// while an installed extension declares it renders that file type. Which commits the two sides read
// from stays the panel's decision.
import { describe, it, expect, afterEach, vi } from 'vitest';
import { render, cleanup, fireEvent } from '@testing-library/svelte';
import DiffViewer from '$lib/components/DiffViewer.svelte';
import { uiInjector } from '$lib/services/ui-injector.svelte';
import type { DiffFile } from '$lib/types/diff';
import type { Extension } from '$lib/types';

vi.mock('svelte/transition', () => ({ slide: vi.fn(), fade: vi.fn() }));

const source = { project: 'p1', task: 't1', working: { old: 'HEAD' } };

function binaryFile(path: string, extra: Partial<DiffFile> = {}): DiffFile {
    return { path, rows: [], added: 0, removed: 0, isBinary: true, fileSize: 8192, ...extra };
}

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

function openViewer(files: DiffFile[]) {
    return render(DiffViewer, { props: { stagedFiles: [], unstagedFiles: files, imageSource: source } });
}

const previewButtons = (container: HTMLElement) => [...container.querySelectorAll<HTMLElement>('.diff-preview-binary-btn')];

afterEach(() => {
    cleanup();
    uiInjector.registerFromExtensions([]);
});

describe('a binary row an extension renders', () => {
    it('offers Preview beside the size, and mounts its frame on that one file', async () => {
        uiInjector.registerFromExtensions([viewerExtension(['zbin'])]);
        const { container } = openViewer([binaryFile('mesh/gear.zbin')]);

        const [button] = previewButtons(container);
        expect(button, 'no Preview button on a declared file type').toBeTruthy();
        await fireEvent.click(button);

        const dialog = container.querySelector('[role="dialog"]');
        expect(dialog).toBeTruthy();
        expect(dialog!.textContent).toContain('mesh/gear.zbin');
        const frame = dialog!.querySelector('iframe');
        expect(frame, 'the extension frame was never mounted').toBeTruthy();
        const src = new URL(frame!.getAttribute('src')!, 'http://x');
        expect(src.pathname).toBe('/extensions/test/mesh-viewer/');
        expect(src.searchParams.get('extch'), 'a frame mounted without a channel token has no armed grants').toBeTruthy();
    });

    it('offers nothing for a file type nobody declared', () => {
        uiInjector.registerFromExtensions([viewerExtension(['zbin'])]);
        const { container } = openViewer([binaryFile('vendor/blob.bin')]);

        expect(previewButtons(container)).toEqual([]);
    });

    it('offers nothing at all when no extension is installed', () => {
        uiInjector.registerFromExtensions([]);
        const { container } = openViewer([binaryFile('mesh/gear.zbin')]);

        expect(previewButtons(container)).toEqual([]);
    });

    it('offers nothing for a file this change removed, which has no new side to read', () => {
        uiInjector.registerFromExtensions([viewerExtension(['zbin'])]);
        const { container } = openViewer([binaryFile('mesh/gear.zbin', { isDeleted: true })]);

        expect(previewButtons(container)).toEqual([]);
    });

    it('leaves the picture and listing controls a declared viewer does not touch', () => {
        uiInjector.registerFromExtensions([viewerExtension(['zbin'])]);
        const { container } = openViewer([binaryFile('docs/logo.png'), binaryFile('mesh/gear.zbin')]);

        expect(container.querySelectorAll('.diff-view-image-btn').length).toBe(1);
        expect(previewButtons(container).length).toBe(1);
    });
});
