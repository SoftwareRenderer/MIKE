import { describe, expect, it } from 'vitest';
import {
    effectiveConfigFields,
    HOST_OWNED_LSP_CONFIG_KEY,
    initialEditConfig,
    isLspExtension,
} from '../extension-config';
import type { Extension, ExtensionType } from '$lib/types';

function makeExt(overrides: Partial<Extension> = {}): Extension {
    return {
        id: 'test-ext',
        name: 'test-ext',
        version: '1.0.0',
        type: 'lsp',
        manifest: {
            display_name: 'Test',
            description: 'test',
            author: 'test',
            version: '1.0.0',
        },
        config: {},
        container_config: {},
        sandbox_config: { network_isolation: true, memory_limit: 0 },
        install_source: 'registry',
        enabled: true,
        running: false,
        installed_at: '2025-01-01T00:00:00Z',
        built_at: '2025-01-01T00:00:00Z',
        ...overrides,
    };
}

describe('isLspExtension', () => {
    it('is true for lsp, false for other types, safe on null', () => {
        expect(isLspExtension(makeExt({ type: 'lsp' }))).toBe(true);
        expect(isLspExtension(makeExt({ type: 'ui' } as { type: ExtensionType }))).toBe(false);
        expect(isLspExtension(null)).toBe(false);
        expect(isLspExtension(undefined)).toBe(false);
    });
});

describe('effectiveConfigFields', () => {
    const schemaWithMemory = {
        properties: [
            { key: 'api_key', type: 'string', title: 'API Key', sensitive: true },
            { key: 'memory_limit', type: 'number', title: 'Memory Limit (MB)', default: 256 },
            { key: 'tsserver_log', type: 'string', title: 'tsserver Log Level', default: 'off' },
        ],
    };

    it('excludes the host-owned memory_limit property for LSP extensions', () => {
        const ext = makeExt({
            type: 'lsp',
            manifest: {
                display_name: 'Test',
                description: 'test',
                author: 'test',
                version: '1.0.0',
                config_schema: schemaWithMemory,
            },
        });
        const fields = effectiveConfigFields(ext);
        expect(fields.map((f) => f.key)).toEqual(['api_key', 'tsserver_log']);
        // Other field metadata is preserved.
        expect(fields[0].sensitive).toBe(true);
        expect(fields[1].default).toBe('off');
    });

    it('keeps memory_limit for non-LSP extensions (host ownership is LSP-only)', () => {
        const ext = makeExt({
            type: 'ui',
            manifest: {
                display_name: 'Test',
                description: 'test',
                author: 'test',
                version: '1.0.0',
                config_schema: schemaWithMemory,
            },
        });
        const keys = effectiveConfigFields(ext).map((f) => f.key);
        expect(keys).toContain(HOST_OWNED_LSP_CONFIG_KEY);
    });

    it('returns [] when there is no schema or no properties', () => {
        expect(effectiveConfigFields(makeExt())).toEqual([]);
        expect(
            effectiveConfigFields({
                ...makeExt(),
                manifest: {
                    display_name: 'T',
                    description: '',
                    author: '',
                    version: '1.0.0',
                    config_schema: { properties: [] },
                },
            })
        ).toEqual([]);
    });
});

describe('initialEditConfig', () => {
    it('seeds memory_limit from the effective sandbox_config value for LSP extensions', () => {
        const ext = makeExt({
            type: 'lsp',
            config: { tsserver_log: 'normal' },
            sandbox_config: { network_isolation: true, memory_limit: 512 },
        });
        expect(initialEditConfig(ext)).toEqual({
            tsserver_log: 'normal',
            memory_limit: 512,
        });
    });

    it('shows 0 (no limit) when the sandbox limit is 0 or missing', () => {
        const zero = makeExt({
            type: 'lsp',
            sandbox_config: { network_isolation: true, memory_limit: 0 },
        });
        expect(initialEditConfig(zero).memory_limit).toBe(0);

        const missing = makeExt({
            type: 'lsp',
            sandbox_config: { network_isolation: true, memory_limit: undefined as unknown as number },
        });
        expect(initialEditConfig(missing).memory_limit).toBe(0);
    });

    it('does not touch the config of non-LSP extensions', () => {
        const ext = makeExt({
            type: 'ui',
            config: { theme: 'dark' },
            sandbox_config: { network_isolation: true, memory_limit: 256 },
        });
        const config = initialEditConfig(ext);
        expect(config).toEqual({ theme: 'dark' });
        expect(config).not.toHaveProperty(HOST_OWNED_LSP_CONFIG_KEY);
    });

    it('does not mutate the original config object', () => {
        const ext = makeExt({
            type: 'lsp',
            config: { tsserver_log: 'off' },
            sandbox_config: { network_isolation: true, memory_limit: 128 },
        });
        const config = initialEditConfig(ext);
        config.memory_limit = 999;
        expect(ext.config).toEqual({ tsserver_log: 'off' });
    });
});
