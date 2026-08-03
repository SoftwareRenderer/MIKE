/**
 * `config.get` — lets an extension read only its OWN configuration.
 *
 * The extension never sees the global app config; it is handed exactly its
 * own `config` object (already decrypted server-side). This capability has no
 * arguments and returns the config as-is.
 */
import type { CapabilityDef, HostContext } from '../types';

export const CAP_CONFIG_GET = 'config.get';

function getConfig(ctx: HostContext): any {
    return ctx.config ?? null;
}

export const configCapabilities: Record<string, CapabilityDef> = {
    [CAP_CONFIG_GET]: {
        kind: 'call',
        handler: getConfig,
    },
};
