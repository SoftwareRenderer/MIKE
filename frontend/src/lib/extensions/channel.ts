/**
 * ExtensionHostChannel — parent-side enforcement for a sandboxed extension iframe.
 *
 * Validates source, grant, args schema, then dispatches to a catalog handler.
 */
import type {
    HostContext,
    HostMessage,
    HostRequest,
    InjectionContext,
    InjectionPoint,
} from './types';
import { SUPPORTED_CAPABILITIES, CAP_EDITOR_ADD_FOOTER_BUTTON, CAP_EDITOR_REMOVE_FOOTER_BUTTON } from './capabilities';
import { cleanupFooterButtons } from './handlers/editor';

export interface ExtensionHostOptions {
    extensionId: string;
    extensionName: string;
    point: InjectionPoint;
    granted: string[];
    config: any;
    context: InjectionContext;
    anchor: HTMLElement | null;
    iframe: HTMLIFrameElement;
}

export class ExtensionHostChannel {
    private iframe: HTMLIFrameElement;
    private opts: ExtensionHostOptions;
    private granted: Set<string>;
    private eventCleanups: Array<() => void> = [];
    private listener: (ev: MessageEvent) => void;
    private destroyed = false;
    private statusEl: HTMLDivElement | null = null;

    /** The injection point this channel is mounted at (public for host bookkeeping). */
    get point(): InjectionPoint {
        return this.opts.point;
    }

    constructor(opts: ExtensionHostOptions) {
        this.opts = opts;
        this.iframe = opts.iframe;
        this.granted = new Set(opts.granted);

        // Implicit grants
        this.granted.add('config.get');
        this.granted.add('log.write');
        if (this.granted.has(CAP_EDITOR_ADD_FOOTER_BUTTON)) {
            this.granted.add(CAP_EDITOR_REMOVE_FOOTER_BUTTON);
        }

        this.listener = (ev: MessageEvent) => this.onMessage(ev);
        window.addEventListener('message', this.listener);
    }

    start() {
        this.post({
            type: 'init',
            extensionId: this.opts.extensionId,
            extensionName: this.opts.extensionName,
            point: this.opts.point,
            granted: [...this.granted],
            config: this.opts.config,
            context: this.opts.context,
        });
        // Set up event listeners for granted event capabilities.
        for (const cap of this.granted) {
            const def = SUPPORTED_CAPABILITIES[cap];
            if (!def || def.kind !== 'event' || !def.onGrant) continue;
            try {
                const cleanup = def.onGrant(this.ctx());
                if (typeof cleanup === 'function') this.eventCleanups.push(cleanup);
            } catch (e: any) {
                console.error(`[extension-host] onGrant(${cap}) failed:`, e);
            }
        }
    }

    forward(cap: string, data: any) {
        this.post({ type: 'event', cap, data });
    }

    destroy() {
        if (this.destroyed) return;
        this.destroyed = true;
        cleanupFooterButtons(this.opts.extensionId, this.opts.anchor);
        window.removeEventListener('message', this.listener);
        for (const fn of this.eventCleanups) {
            try { fn(); } catch {}
        }
        this.eventCleanups = [];
        this.statusEl?.remove();
        this.statusEl = null;
        this.iframe.remove();
    }

    // ── internals ────────────────────────────────────────────────

    private ctx(): HostContext {
        return {
            extensionId: this.opts.extensionId,
            extensionName: this.opts.extensionName,
            point: this.opts.point,
            config: this.opts.config,
            context: this.opts.context,
            anchor: this.opts.anchor,
            sendEvent: (cap, data) => this.forward(cap, data),
            ui: {
                setLoading: (message) => this.renderStatus('loading', message),
                setError: (message) => this.renderStatus('error', message),
                hide: () => this.renderStatus('none'),
                setSize: (height) => { this.iframe.style.height = `${height}px`; },
            },
        };
    }

    private renderStatus(kind: 'loading' | 'error' | 'none', message?: string) {
        if (!this.opts.anchor) return;
        if (kind === 'none') {
            if (this.statusEl) this.statusEl.style.display = 'none';
            return;
        }
        const el = this.statusEl ?? this.createStatusEl();
        el.innerHTML = '';
        el.style.display = 'flex';
        if (kind === 'loading') {
            el.style.background = 'var(--color-surface-3, #1e1e1e)';
            el.style.color = 'var(--color-accent, #00add8)';
            const spin = document.createElement('span');
            spin.style.cssText = 'width:12px;height:12px;flex:none;border:2px solid rgba(0,173,216,.25);border-top-color:var(--color-accent,#00add8);border-radius:50%;animation:ext-spin 1s linear infinite;';
            const txt = document.createElement('span');
            txt.textContent = message || 'Loading…';
            el.appendChild(spin);
            el.appendChild(txt);
            ensureSpinKeyframes();
        } else {
            el.style.background = 'transparent';
            el.style.color = 'var(--color-status-red, #f85149)';
            const txt = document.createElement('span');
            txt.textContent = message || 'Extension error';
            el.appendChild(txt);
        }
    }

    private createStatusEl(): HTMLDivElement {
        const el = document.createElement('div');
        el.setAttribute('data-ext-status', '');
        el.style.cssText = 'display:none;align-items:center;gap:6px;margin-top:4px;padding:4px 8px;border-radius:var(--radius-sm,4px);width:fit-content;font-size:0.75rem;';
        this.opts.anchor?.appendChild(el);
        this.statusEl = el;
        return el;
    }

    private post(msg: HostMessage) {
        if (this.destroyed) return;
        // Round-trip through JSON to strip Svelte $state proxies (not cloneable).
        let plain: HostMessage;
        try {
            plain = JSON.parse(JSON.stringify(msg));
        } catch {
            return;
        }
        // Target origin "null" matches the iframe's opaque origin (sandboxed
        // without allow-same-origin), so messages are NOT delivered if the
        // iframe is ever navigated to a non-opaque (e.g. hijacked) origin.
        // NOTE: target origin "*" is used because the iframe's opaque origin
        // ("null") is not reliably accepted as a targetOrigin by Chromium.
        // The origin is instead enforced on the receive side (see onMessage),
        // where the host rejects any sender that is not opaque-origin.
        this.iframe.contentWindow?.postMessage(plain, '*');
    }

    private async onMessage(ev: MessageEvent) {
        if (this.destroyed) return;
        // Only accept messages from OUR sandboxed iframe: the exact window AND
        // an opaque origin ('null'). A same-frame page that was navigated to a
        // non-opaque (hijacked) origin is rejected even though its window
        // reference matches.
        if (ev.source !== this.iframe.contentWindow) return;
        if (ev.origin !== 'null') return;

        const data = ev.data;
        if (!data || typeof data !== 'object' || data.type !== 'call') return;

        const req = data as HostRequest;
        const id = req.id;
        if (typeof id !== 'string' || !id) return;

        // Grant check.
        if (!this.granted.has(req.cap)) {
            this.post({ type: 'response', id, ok: false, error: `capability not granted: ${req.cap}` });
            return;
        }
        const def = SUPPORTED_CAPABILITIES[req.cap];
        if (!def || def.kind !== 'call' || !def.handler) {
            this.post({ type: 'response', id, ok: false, error: `unsupported capability: ${req.cap}` });
            return;
        }

        // Arg validation.
        const args = req.args ?? {};
        if (def.argsSchema) {
            const err = validateArgs(args, def.argsSchema);
            if (err) {
                this.post({ type: 'response', id, ok: false, error: `invalid args: ${err}` });
                return;
            }
        }

        // Dispatch.
        try {
            const result = await def.handler(this.ctx(), args);
            this.post({ type: 'response', id, ok: true, data: result });
        } catch (e: any) {
            this.post({ type: 'response', id, ok: false, error: e?.message || String(e) });
        }
    }
}

let spinKeyframesInjected = false;
function ensureSpinKeyframes() {
    if (spinKeyframesInjected) return;
    spinKeyframesInjected = true;
    const style = document.createElement('style');
    style.textContent = '@keyframes ext-spin{from{transform:rotate(0deg)}to{transform:rotate(360deg)}}';
    document.head.appendChild(style);
}

function validateArgs(args: any, schema: Record<string, 'string' | 'number' | 'boolean' | 'object' | 'array' | 'any'>): string | null {
    for (const [key, type] of Object.entries(schema)) {
        const v = args[key];
        if (v === undefined || v === null) return `missing '${key}'`;
        switch (type) {
            case 'string': if (typeof v !== 'string') return `'${key}' must be a string`; break;
            case 'number': if (typeof v !== 'number') return `'${key}' must be a number`; break;
            case 'boolean': if (typeof v !== 'boolean') return `'${key}' must be a boolean`; break;
            case 'object': if (typeof v !== 'object' || v === null || Array.isArray(v)) return `'${key}' must be an object`; break;
            case 'array': if (!Array.isArray(v)) return `'${key}' must be an array`; break;
            case 'any': break;
        }
    }
    return null;
}
