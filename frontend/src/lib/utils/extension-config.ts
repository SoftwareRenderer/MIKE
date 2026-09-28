import type { Extension, SchemaProperty } from '$lib/types';
import { memoryLimitToInput, parseMemoryLimitMB } from './memory-stats';

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

/**
 * The host folders an agent extension may see are saved by their own endpoint, so the edit form shows no
 * field for `sandbox_mount` and never sends it.
 */
const HOST_OWNED_AGENT_CONFIG_KEY = 'sandbox_mount';

/** @knipignore Which extension type this is decides every other rule here, so it is asserted alone. */
export function isLspExtension(ext: Extension | null | undefined): boolean {
    return ext?.type === 'lsp';
}

function isAgentExtension(ext: Extension | null | undefined): boolean {
    return ext?.type === 'agent';
}

/**
 * The config fields the edit form shows. Fields the app manages itself are left out: `memory_limit` for
 * language servers, `sandbox_mount` for agents.
 */
export function effectiveConfigFields(ext: Extension | null | undefined): ConfigField[] {
    const schema = ext?.manifest.config_schema;
    if (!schema?.properties) return [];
    return schema.properties
        .filter((p) => !(isLspExtension(ext) && p.key === HOST_OWNED_LSP_CONFIG_KEY))
        .filter((p) => !(isAgentExtension(ext) && p.key === HOST_OWNED_AGENT_CONFIG_KEY))
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
    if (isAgentExtension(ext)) {
        // Deleted, not just hidden: sending the field back counts as a write, which the server refuses.
        delete config[HOST_OWNED_AGENT_CONFIG_KEY];
    }
    return config;
}

/**
 * The number a form field opens with. A memory limit renders blank for "no limit", where every other
 * field shows its stored value or the schema default.
 */
export function configNumberValue(config: Record<string, any>, field: ConfigField): number | null {
    const raw = config[field.key] ?? field.default;
    return field.key === HOST_OWNED_LSP_CONFIG_KEY ? memoryLimitToInput(raw) : raw;
}

/** What typing in that field means: blank, 0 or a negative limit is the numeric 0 (unlimited). */
export function configNumberInput(key: string, raw: string): number {
    return key === HOST_OWNED_LSP_CONFIG_KEY ? parseMemoryLimitMB(raw) : Number(raw);
}
