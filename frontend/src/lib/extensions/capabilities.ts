/**
 * Capability catalog — the single source of truth for sandboxed UI extensions.
 *
 * The host grants an extension the intersection of its manifest `permissions`
 * and this catalog. Anything not here is unreachable.
 */
import type { CapabilityDef } from './types';
import { createTaskCapabilities } from './handlers/createTask';
import { networkCapabilities } from './handlers/network';
import { configCapabilities } from './handlers/config';
import { logCapabilities } from './handlers/log';
import { uiCapabilities } from './handlers/ui';
import { storageCapabilities } from './handlers/storage';

export {
    CAP_CREATE_TASK_INSERT_TEXT,
    CAP_CREATE_TASK_PASTE,
} from './handlers/createTask';
export {
    CAP_NETWORK_PROXY,
} from './handlers/network';
export {
    CAP_CONFIG_GET,
} from './handlers/config';
export {
    CAP_LOG_WRITE,
} from './handlers/log';
export {
    CAP_UI_STATUS,
    CAP_UI_SET_SIZE,
} from './handlers/ui';
export {
    CAP_STORAGE_GET,
    CAP_STORAGE_SET,
    CAP_STORAGE_DELETE,
    CAP_STORAGE_LIST,
} from './handlers/storage';

/** Fully-qualified capability name → definition. */
export const SUPPORTED_CAPABILITIES: Record<string, CapabilityDef> = {
    ...createTaskCapabilities,
    ...networkCapabilities,
    ...configCapabilities,
    ...logCapabilities,
    ...uiCapabilities,
    ...storageCapabilities,
};

/**
 * Compute the set of capabilities to grant an extension.
 * Always returns a subset of the manifest's requested permissions that exist
 * in the catalog — least privilege. Unknown/unsupported names are dropped.
 */
export function computeGrants(requested: string[] | undefined): string[] {
    if (!requested || !Array.isArray(requested)) return [];
    const seen = new Set<string>();
    const out: string[] = [];
    for (const name of requested) {
        if (SUPPORTED_CAPABILITIES[name] && !seen.has(name)) {
            seen.add(name);
            out.push(name);
        }
    }
    return out;
}
