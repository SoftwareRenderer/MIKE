import { describe, it, expect } from 'vitest';
import { ExtensionDraft } from '../extensions-draft.svelte';
import type { Extension } from '$lib/types';

/**
 * The Edit dialog's draft, held by nobody but the dialog: the grant stays a wanted path until Save,
 * the dependency suggestion fills an empty field once and never overwrites a typed one, and a probe
 * answer belongs to the dialog rather than to the record it probed.
 */

function lspExtension(overrides: Partial<Extension> = {}): Extension {
    return {
        id: 'ext-1',
        name: 'gopls',
        author: 'go-team',
        version: '0.7.0',
        type: 'lsp',
        manifest: {
            display_name: 'gopls',
            description: 'Go language server',
            capabilities: { lsp: { language: 'go' } },
        },
        config: {},
        container_config: {},
        sandbox_config: {
            memory_limit: 1024,
            dependency_cache: { container_path: '/go/pkg/mod', env: 'GOMODCACHE' },
        },
        install_source: 'registry',
        enabled: true,
        running: false,
        installed_at: '2026-01-01T00:00:00Z',
        built_at: '',
        ...overrides,
    } as unknown as Extension;
}

/** The same record with a different manifest capabilities map. */
function withManifest(ext: Extension, capabilities: Record<string, any>): Extension {
    return { ...ext, manifest: { ...ext.manifest, capabilities } as Extension['manifest'] };
}

function agentExtension(): Extension {
    return {
        id: 'ext-2',
        name: 'pi',
        author: 'pi',
        version: '1.0.0',
        type: 'agent',
        manifest: {
            display_name: 'pi',
            description: 'agent adapter',
            capabilities: { agent: { sandbox: { mount: [{ host_path: '~/.pi/agent', container_path: '/root/.pi/agent', mode: 'rw' }] } } },
        },
        config: { sandbox_mount: [{ host_path: '/home/tester/.pi/agent', container_path: '/root/.pi/agent', mode: 'ro' }] },
        container_config: {},
        sandbox_config: {},
        install_source: 'registry',
        enabled: true,
        running: false,
        installed_at: '2026-01-01T00:00:00Z',
        built_at: '',
    } as unknown as Extension;
}

describe('opening the dialog', () => {
    it('seeds every field from the stored record', () => {
        const draft = new ExtensionDraft();
        draft.open(lspExtension());

        expect(draft.extension?.id).toBe('ext-1');
        expect(draft.config.memory_limit).toBe(1024);
        expect(draft.dependency).toBe('');
        expect(draft.dependencyError).toBe('');
        expect(draft.saving).toBe(false);
    });

    it('reads the LSP gates with chat diagnostics off', () => {
        const draft = new ExtensionDraft();
        draft.open(lspExtension());
        expect(draft.lsp).toEqual({ editor_enabled: true, tool_calls_enabled: true, chat_diagnostics_enabled: false });

        draft.open(withManifest(lspExtension(), { lsp: { editor_enabled: false, chat_diagnostics_enabled: true } }));
        expect(draft.lsp).toEqual({ editor_enabled: false, tool_calls_enabled: true, chat_diagnostics_enabled: true });
    });

    it('opens the folder editor on what is already approved', () => {
        const draft = new ExtensionDraft();
        draft.open(agentExtension());
        expect(draft.mounts).toEqual([
            { host_path: '/home/tester/.pi/agent', container_path: '/root/.pi/agent', mode: 'ro', suggestion: '~/.pi/agent' },
        ]);
    });

    it('sends only the rows with a host folder', () => {
        const draft = new ExtensionDraft();
        draft.open(agentExtension());
        draft.mounts[0].host_path = '   ';
        expect(draft.mountsToSend()).toEqual([]);
        draft.setMountHost(0, '/srv/agent');
        expect(draft.mountsToSend()).toEqual([{ host_path: '/srv/agent', container_path: '/root/.pi/agent', mode: 'ro' }]);
    });
});

describe('the dependency suggestion', () => {
    it('is empty until the server reports its home', () => {
        const draft = new ExtensionDraft();
        draft.open(lspExtension());
        expect(draft.dependency).toBe('');
        expect(draft.wantsHomeDirectory()).toBe(true);
        expect(draft.wantsHomeDirectory()).toBe(false);

        draft.setHomeDirectory('/home/tester');
        expect(draft.dependency).toBe('/home/tester/go/pkg/mod');
    });

    it('fills the field once and never overwrites what someone typed', () => {
        const draft = new ExtensionDraft();
        draft.open(lspExtension());
        draft.dependency = '/data/mine';
        draft.setHomeDirectory('/home/tester');
        expect(draft.dependency).toBe('/data/mine');
    });

    it('is not applied to a later dialog whose field already holds a grant', () => {
        const draft = new ExtensionDraft();
        draft.open(lspExtension());
        draft.setHomeDirectory('/home/tester');
        draft.open(lspExtension({
            sandbox_config: {
                memory_limit: 0,
                dependency_cache: { container_path: '/go/pkg/mod', env: 'GOMODCACHE' },
                dependency_host_path: '/data/shared/gomodcache',
            },
        } as unknown as Extension));
        expect(draft.dependency).toBe('/data/shared/gomodcache');
        draft.setHomeDirectory('/elsewhere');
        expect(draft.dependency).toBe('/data/shared/gomodcache');
    });

    it('opens with the suggestion when the home directory was read before the dialog opened', () => {
        const draft = new ExtensionDraft();
        draft.setHomeDirectory('/home/tester');
        draft.open(lspExtension());
        expect(draft.dependency).toBe('/home/tester/go/pkg/mod');
    });
});

describe('the address probe', () => {
    it('holds the answer per field and refuses a second probe while one runs', () => {
        const draft = new ExtensionDraft();
        draft.open(lspExtension());

        draft.beginProbe('base_url');
        expect(draft.probingKey).toBe('base_url');
        draft.answerProbe('base_url', { success: true, message: 'reachable' });
        expect(draft.probingKey).toBe('');
        expect(draft.probeAnswers.base_url).toEqual({ success: true, message: 'reachable' });
    });

    it('drops the earlier answer for a field being probed again', () => {
        const draft = new ExtensionDraft();
        draft.open(lspExtension());
        draft.answerProbe('base_url', { success: true, message: 'reachable' });
        draft.beginProbe('base_url');
        expect(draft.probeAnswers.base_url).toBeUndefined();
    });

    it('is forgotten when the dialog closes', () => {
        const draft = new ExtensionDraft();
        draft.open(lspExtension());
        draft.answerProbe('base_url', { success: false, message: 'refused' });
        draft.close();
        expect(draft.extension).toBeNull();
        expect(draft.probeAnswers).toEqual({});
        expect(draft.dependency).toBe('');
        expect(draft.mounts).toEqual([]);
    });
});
