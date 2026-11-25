import { describe, expect, it } from 'vitest';
import {
    configChoiceValue,
    effectiveConfigFields,
    HOST_OWNED_LSP_CONFIG_KEY,
    initialEditConfig,
    isChoiceField,
    isLspExtension,
    type ConfigField,
} from '../extension-config';
import type { Extension, ExtensionType, SchemaProperty } from '$lib/types';

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

describe('choice fields', () => {
    const levelField: ConfigField = {
        key: 'level',
        type: 'string',
        title: 'Verbosity',
        enum: ['off', 'terse', 'verbose'],
        default: 'off',
    };

    function withSchema(properties: object[], config: Record<string, any>): Extension {
        return makeExt({
            type: 'ui',
            config,
            // One fixture declares no type at all, which the form reads as a string.
            manifest: { display_name: 'T', description: '', author: '', version: '1.0.0', config_schema: { properties: properties as unknown as SchemaProperty[] } },
        });
    }

    const levelSchema = [{ key: 'level', type: 'string', enum: ['off', 'terse'], default: 'off' }];

    it('offers a string list as a pick-list and every other type as its own field', () => {
        expect(isChoiceField(levelField)).toBe(true);
        expect(isChoiceField({ ...levelField, enum: [] })).toBe(false);
        expect(isChoiceField({ ...levelField, enum: undefined })).toBe(false);
        expect(isChoiceField({ ...levelField, type: 'number' })).toBe(false);
        expect(isChoiceField({ ...levelField, type: 'url' })).toBe(false);
    });

    it('shows the stored choice, and the schema default when the list no longer declares it', () => {
        expect(configChoiceValue({ level: 'terse' }, levelField)).toBe('terse');
        expect(configChoiceValue({ level: 'chatty' }, levelField)).toBe('off');
        expect(configChoiceValue({ level: null }, levelField)).toBe('off');
        expect(configChoiceValue({}, levelField)).toBe('off');
    });

    it('writes the fallback into the draft, so Save is never refused for a word nobody typed', () => {
        expect(initialEditConfig(withSchema(levelSchema, { level: 'chatty' }))).toEqual({ level: 'off' });
    });

    it('leaves a choice nobody has made out of the draft, where the server reads it as unset', () => {
        expect(initialEditConfig(withSchema(levelSchema, {}))).not.toHaveProperty('level');
    });

    it('reads a property that declares no type as a string', () => {
        const untyped = [{ key: 'level', enum: ['off', 'terse'], default: 'off' }];
        expect(initialEditConfig(withSchema(untyped, { level: 'chatty' }))).toEqual({ level: 'off' });
    });

    it('holds a list on a number to nothing — the field keeps the control its type names', () => {
        const numbers = [{ key: 'port', type: 'number', enum: [8080], default: 8080 }];
        expect(initialEditConfig(withSchema(numbers, { port: 9090 }))).toEqual({ port: 9090 });
    });
});
