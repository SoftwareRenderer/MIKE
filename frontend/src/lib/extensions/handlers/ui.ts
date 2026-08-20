/**
 * `ui.*` — host-rendered loading/error indicators and iframe sizing.
 * The iframe is invisible (height 0) by default; the extension uses these
 * to surface status or reveal custom content.
 */
import type { CapabilityDef, HostContext } from '../types';
import { showExtensionModal } from '../modal';

export const CAP_UI_STATUS = 'ui.status';
export const CAP_UI_SET_SIZE = 'ui.setSize';
export const CAP_UI_MODAL = 'ui.modal';

export const uiCapabilities: Record<string, CapabilityDef> = {
    [CAP_UI_STATUS]: {
        kind: 'call',
        argsSchema: { state: 'string' },
        handler: (ctx: HostContext, args: { state: string, message?: string }) => {
            switch (args.state) {
                case 'loading': ctx.ui.setLoading(args.message || ''); break;
                case 'error': ctx.ui.setError(args.message || ''); break;
                case 'hidden': ctx.ui.hide(); break;
                default: throw new Error(`unknown ui.status state: ${args.state}`);
            }
        },
    },
    [CAP_UI_MODAL]: {
        kind: 'call',
        argsSchema: { message: 'string' },
        // Resolves when the user dismisses the modal — `extHost.modal()` is
        // an "await the user acknowledged" primitive.
        handler: (ctx: HostContext, args: { message: string, title?: string, confirmLabel?: string }) =>
            showExtensionModal({
                title: args.title,
                message: args.message,
                confirmLabel: args.confirmLabel,
                extensionName: ctx.extensionName,
            }),
    },
    [CAP_UI_SET_SIZE]: {
        kind: 'call',
        argsSchema: { height: 'number' },
        handler: (ctx: HostContext, args: { height: number }) => ctx.ui.setSize(args.height),
    },
};
