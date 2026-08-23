import type { Extension, SchemaProperty } from '$lib/types';

export interface ConfigField {
    key: string;
    title: string;
    type: string;
    description?: string;
    default?: any;
    sensitive?: boolean;
    enum?: any[];
}

/**
 * The sandbox memory limit is host-owned for LSP extensions: the extension
 * sandbox manager enforces it, and every LSP extension gets the option —
 * individual extensions must not (and need not) declare it. The edit form
 * renders it in its own Sandbox section, seeded from `sandbox_config`.
 */
export const HOST_OWNED_LSP_CONFIG_KEY = 'memory_limit';

export function isLspExtension(ext: Extension | null | undefined): boolean {
    return ext?.type === 'lsp';
}

/**
 * Config fields the extension edit form should render for an extension.
 * For LSP extensions any manifest-declared `memory_limit` schema property is
 * excluded — the host renders its own field instead (no double rendering).
 */
export function effectiveConfigFields(ext: Extension | null | undefined): ConfigField[] {
    const schema = ext?.manifest.config_schema;
    if (!schema?.properties) return [];
    return schema.properties
        .filter((p) => !(isLspExtension(ext) && p.key === HOST_OWNED_LSP_CONFIG_KEY))
        .map((p: SchemaProperty) => ({
            key: p.key,
            title: p.title || p.key,
            description: p.description,
            type: p.type || 'string',
            default: p.default,
            sensitive: p.sensitive || false,
            enum: p.enum,
        }));
}

/**
 * Initial values for the extension edit form. For LSP extensions,
 * `memory_limit` is seeded from the effective `sandbox_config` value — the
 * host-owned source of truth — so the form always shows the limit actually
 * in force (0 = no limit), regardless of any legacy schema default.
 */
export function initialEditConfig(ext: Extension): Record<string, any> {
    const config: Record<string, any> = { ...ext.config };
    if (isLspExtension(ext)) {
        config[HOST_OWNED_LSP_CONFIG_KEY] = ext.sandbox_config?.memory_limit ?? 0;
    }
    return config;
}
