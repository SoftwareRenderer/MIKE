/**
 * Which files an extension is mounted for. A frame is armed with its grants the moment it is built, so
 * mounting a byte-capable extension for a file no installed extension can draw opens a channel for
 * nothing. `ui.file_suffixes`, read back off the stored manifest, is what decides it.
 */
import { describe, it, expect } from 'vitest';
import { UIInjector } from '$lib/services/ui-injector.svelte';
import { previewableBinary, fileSuffixOf } from '$lib/utils/binary-preview';
import type { Extension, UIExtensionConfig } from '$lib/types';

function uiExtension(name: string, ui: UIExtensionConfig): Extension {
    return {
        id: `ext-${name}`,
        name,
        version: '1.0.0',
        type: 'ui',
        enabled: true,
        manifest: {
            display_name: name,
            description: 'Test',
            author: 'Test',
            version: '1.0.0',
            capabilities: { ui },
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

function injectorWith(...extensions: Extension[]): UIInjector {
    const injector = new UIInjector();
    injector.registerFromExtensions(extensions);
    return injector;
}

const viewer = uiExtension('viewer', { injection_points: ['fileEditor'], file_suffixes: ['zbin'] });
const general = uiExtension('general', { injection_points: ['fileEditor'] });

const FILE_EDITOR = 'fileEditor' as any;
const TASK_CARD = 'taskCard' as any;

const mount = (filePath: string) => ({ component: 'FileEditor', filePath });

describe('mounting by declared suffix', () => {
    it('mounts a viewer for the file type it declared', () => {
        const injector = injectorWith(viewer);
        const regs = injector.getRegistrations(FILE_EDITOR, mount('/repo/gear.zbin'));
        expect(regs.map((r) => r.extensionName)).toEqual(['viewer']);
    });

    it('does not mount a viewer for a file type it never declared', () => {
        const injector = injectorWith(viewer);
        const regs = injector.getRegistrations(FILE_EDITOR, mount('/repo/report.md'));
        expect(regs).toEqual([]);
    });

    it('mounts an extension that declared no suffixes everywhere its point does', () => {
        const injector = injectorWith(general);
        expect(injector.getRegistrations(FILE_EDITOR, mount('/repo/report.md')).length).toBe(1);
        expect(injector.getRegistrations(FILE_EDITOR, mount('/repo/gear.zbin')).length).toBe(1);
    });

    it('leaves a mount that names no file alone, because there is no file to read from it', () => {
        // One extension doing both jobs: previewing meshes and marking up cards.
        const both = uiExtension('viewer', { injection_points: ['fileEditor', 'taskCard'], file_suffixes: ['zbin'] });
        const injector = injectorWith(both);
        expect(injector.getRegistrations(TASK_CARD, { component: 'TaskCard' }).length).toBe(1);
        expect(injector.getRegistrations(FILE_EDITOR, { component: 'FileEditor' }).length).toBe(1);
        expect(injector.getRegistrations(FILE_EDITOR, mount('/repo/report.md')).length).toBe(0);
    });

    it('keeps a declared viewer out of another extension’s mount list', () => {
        const injector = injectorWith(viewer, general);
        const markdown = injector.getRegistrations(FILE_EDITOR, mount('/repo/report.md'));
        expect(markdown.map((r) => r.extensionName)).toEqual(['general']);
        const mesh = injector.getRegistrations(FILE_EDITOR, mount('/repo/gear.zbin'));
        expect(mesh.map((r) => r.extensionName).sort()).toEqual(['general', 'viewer']);
    });

    it('counts a disabled row as no declaration at all', () => {
        const injector = injectorWith({ ...viewer, enabled: false } as Extension);
        expect(injector.getRegistrations(FILE_EDITOR, mount('/repo/gear.zbin'))).toEqual([]);
    });
});

describe('the affordance question', () => {
    it('offers nothing when no extension declared the type', () => {
        expect(previewableBinary('/repo/gear.zbin', [])).toBe(false);
        expect(previewableBinary('/repo/gear.zbin', [{ fileSuffixes: ['md'] }])).toBe(false);
    });

    it('offers it for exactly the type that was declared', () => {
        expect(previewableBinary('/repo/gear.ZBIN', [{ fileSuffixes: ['zbin'] }])).toBe(true);
        expect(previewableBinary('/repo/gear.stl', [{ fileSuffixes: ['zbin'] }])).toBe(false);
    });

    it('offers nothing for a name with no suffix, which nobody could have declared', () => {
        expect(previewableBinary('/repo/Makefile', [{ fileSuffixes: ['zbin'] }])).toBe(false);
        expect(previewableBinary('/repo/.gitignore', [{ fileSuffixes: ['gitignore'] }])).toBe(false);
    });

    it('reads the file off the mount context, project-relative first', () => {
        expect(fileSuffixOf({ component: 'FileEditor', repoPath: 'a/b/part.zbin', filePath: '/abs/x' })).toBe('zbin');
        expect(fileSuffixOf({ component: 'FileEditor', filePath: '/abs/x.md' })).toBe('md');
        expect(fileSuffixOf({ component: 'TaskCard' })).toBe('');
    });
});
