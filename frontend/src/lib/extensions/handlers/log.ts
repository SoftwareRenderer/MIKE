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
    // The tagged line is built inside the `console.*` call, not in a local: the line is
    // a log line wherever it goes, and a `prefix` variable makes it look like a string
    // the UI might render (it is what `i18n-count.py` reads as copy).
    switch (level) {
        case 'debug': console.debug(`[ext:${ctx.extensionName}] ${args.message}`); break;
        case 'warn': console.warn(`[ext:${ctx.extensionName}] ${args.message}`); break;
        case 'error': console.error(`[ext:${ctx.extensionName}] ${args.message}`); break;
        default: console.log(`[ext:${ctx.extensionName}] ${args.message}`);
    }
}

export const logCapabilities: Record<string, CapabilityDef> = {
    [CAP_LOG_WRITE]: {
        kind: 'call',
        argsSchema: { level: 'string', message: 'string' },
        handler: writeLog,
    },
};
