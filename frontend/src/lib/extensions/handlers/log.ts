/**
 * `log.write` — a safe, namespaced logging capability.
 *
 * Extensions cannot call console directly in a useful, identifiable way from
 * the sandbox, and we want log lines tagged with the extension name. This
 * capability is the only logging path.
 */
import type { CapabilityDef, HostContext } from '../types';

export const CAP_LOG_WRITE = 'log.write';

function writeLog(ctx: HostContext, args: { level: string; message: string }): void {
    const level = args.level || 'info';
    const prefix = `[ext:${ctx.extensionName}] ${args.message}`;
    switch (level) {
        case 'debug': console.debug(prefix); break;
        case 'warn': console.warn(prefix); break;
        case 'error': console.error(prefix); break;
        default: console.log(prefix);
    }
}

export const logCapabilities: Record<string, CapabilityDef> = {
    [CAP_LOG_WRITE]: {
        kind: 'call',
        argsSchema: { level: 'string', message: 'string' },
        handler: writeLog,
    },
};
