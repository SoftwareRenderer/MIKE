/**
 * ExtensionHostChannel — parent-side enforcement for a sandboxed extension iframe.
 *
 * Validates source, grant, args schema, then dispatches to a catalog handler.
 */
import type {
    HostContext,
    HostHello,
    HostMessage,
    HostRequest,
    InjectionContext,
    InjectionPoint,
} from './types';
import {
    SUPPORTED_CAPABILITIES,
    CAP_EDITOR_ADD_FOOTER_BUTTON,
    CAP_EDITOR_REMOVE_FOOTER_BUTTON,
    CAP_THEME_GET,
    CAP_UI_STATUS,
    CAP_EDITOR_GET_CONTENT,
    CAP_EDITOR_CONTENT_CHANGE,
    CAP_EDITOR_SET_OVERLAY,
    CAP_PREVIEW_SERVE,
    CAP_PREVIEW_SERVE_SCRIPTS,
    CAP_PREVIEW_SERVE_NETWORK,
    CAP_PREVIEW_RELEASE,
    CAP_PREVIEW_NAV_STATE,
} from './capabilities';
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
    /**
     * One-time channel token. It is also carried in the iframe URL as `extch`,
     * so the document the host mounted knows it and any other document in that
     * frame does not. The channel stays disarmed until it is echoed back
     * (see the handshake note in types.ts).
     */
    channelToken: string;
}

/**
 * Mint a per-mount channel token (128-bit, hex).
 *
 * Not a secret from the frame's own point of view (it is in that document's
 * URL) — it is only unguessable/unreadable for a *different* document that ends
 * up in the same frame.
 */
export function mintChannelToken(): string {
    const bytes = new Uint8Array(16);
    if (typeof crypto !== 'undefined' && crypto.getRandomValues) {
        crypto.getRandomValues(bytes);
    } else {
        // Non-browser fallback (jsdom without crypto.getRandomValues).
        for (let i = 0; i < bytes.length; i++) bytes[i] = Math.floor(Math.random() * 256);
    }
    let out = '';
    for (const b of bytes) out += b.toString(16).padStart(2, '0');
    return out;
}

export class ExtensionHostChannel {
    /** Capability-call budget: see `overBudget()`. Comfortably above what a
     * keystroke-driven extension does (a few calls per edit) while still
     * capping a runaway loop. */
    private static readonly CALL_LIMIT = 60;
    private static readonly CALL_WINDOW_MS = 1000;

    private iframe: HTMLIFrameElement;
    private opts: ExtensionHostOptions;
    private granted: Set<string>;
    private eventCleanups: Array<() => void> = [];
    private listener: (ev: MessageEvent) => void;
    private destroyed = false;
    private statusEl: HTMLDivElement | null = null;
    /** True between a valid handshake and the next frame load: only then are
     * `call`s dispatched and host→frame payloads posted. */
    private armed = false;
    /** `load` events seen on the frame. The mounted document is a single
     * document, so anything above 1 means the frame navigated. */
    private frameLoads = 0;
    /** Sliding-window capability budget (see `overBudget`). */
    private callTimes: number[] = [];
    private lastRateWarnAt = 0;

    /** The injection point this channel is mounted at (public for host bookkeeping). */
    get point(): InjectionPoint {
        return this.opts.point;
    }

    constructor(opts: ExtensionHostOptions) {
        this.opts = opts;
        this.iframe = opts.iframe;
        this.granted = new Set(opts.granted);

        // Implicit and safe grants
        this.granted.add('config.get');
        this.granted.add('log.write');
        this.granted.add(CAP_THEME_GET);
        this.granted.add(CAP_UI_STATUS);
        if (this.granted.has(CAP_EDITOR_ADD_FOOTER_BUTTON)) {
            this.granted.add(CAP_EDITOR_REMOVE_FOOTER_BUTTON);
        }
        if (this.granted.has(CAP_EDITOR_GET_CONTENT)) {
            this.granted.add(CAP_EDITOR_CONTENT_CHANGE);
        }
        if (this.granted.has(CAP_EDITOR_SET_OVERLAY)) {
            this.granted.add(CAP_PREVIEW_SERVE);
            this.granted.add(CAP_PREVIEW_RELEASE);
        }
        // The watchdog's decision channel rides along with any preview
        // capability: it only reports what the server served under the
        // extension's own tokens.
        if (
            this.granted.has(CAP_PREVIEW_SERVE) ||
            this.granted.has(CAP_PREVIEW_SERVE_SCRIPTS) ||
            this.granted.has(CAP_PREVIEW_SERVE_NETWORK) ||
            this.granted.has(CAP_PREVIEW_RELEASE)
        ) {
            this.granted.add(CAP_PREVIEW_NAV_STATE);
        }

        this.listener = (ev: MessageEvent) => this.onMessage(ev);
        window.addEventListener('message', this.listener);
    }

    /**
     * Handle a `load` of the extension iframe. The frame's document is NOT
     * re-read here (an opaque-origin frame's URL is unreadable), so identity is
     * re-established by handshake instead:
     *
     * - 1st load: ask the mounted document to prove itself.
     * - 2nd+ load: the frame navigated. A UI extension entry document is a
     *   single document, so this is always unexpected. Warn and DISARM (stop
     *   answering calls, stop pushing events/config) and ask again — a genuine
     *   extension reload simply re-handshakes and keeps working. Deliberately
     *   no teardown: the watchdog is diagnostic, the handshake is the control.
     *
     * Residual, by design: a document that was legitimately armed and then
     * *chooses* to forward `extch` to its navigation target hands the channel to
     * that target. That needs an already-scripted extension document willing to
     * leak its own channel — the warning above is the signal for it.
     */
    notifyFrameLoad(): void {
        if (this.destroyed) return;
        this.frameLoads++;
        if (this.frameLoads > 1) {
            console.warn(
                `[extension-host] ${this.opts.extensionName}: extension frame reloaded ` +
                `(${this.frameLoads} loads) — channel disarmed until it re-handshakes.`
            );
            this.disarm();
        }
        this.post({ type: 'exthello-request' });
    }

    /** Valid handshake: send `init` once and attach the event capabilities. */
    private arm(): void {
        if (this.armed) return;
        this.armed = true;
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

    /**
     * Back to disarmed: tear down every host-side listener that would push data
     * into the frame (editor content, paste events, …) and stop responding to
     * calls, until a fresh handshake arrives.
     */
    private disarm(): void {
        this.armed = false;
        for (const fn of this.eventCleanups) {
            try { fn(); } catch {}
        }
        this.eventCleanups = [];
    }

    private onHello(msg: HostHello): void {
        if (this.armed) return;
        if (typeof msg.token !== 'string' || msg.token !== this.opts.channelToken) {
            console.warn(
                `[extension-host] ${this.opts.extensionName}: channel handshake rejected ` +
                `(document did not arrive via the mounted URL).`
            );
            return;
        }
        this.arm();
    }

    /** Push an app event into the frame. Dropped while disarmed (post()). */
    forward(cap: string, data: any) {
        this.post({ type: 'event', cap, data });
    }

    destroy() {
        if (this.destroyed) return;
        this.destroyed = true;
        this.armed = false;
        window.removeEventListener('message', this.listener);
        cleanupFooterButtons(this.opts.extensionId, this.opts.anchor);
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
                // Fill the anchor element, which already fills the editor content
                // area (absolute inset:0), so the iframe tracks editor/panel
                // resizes automatically instead of holding a stale pixel height.
                setFill: () => { this.iframe.style.height = '100%'; },
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

    /**
     * Sliding-window capability budget: CALL_LIMIT calls per CALL_WINDOW_MS per
     * mounted extension instance. Handlers can hit the network (`network.proxy`,
     * `storage.*`, `preview.serve`) and mutate the user's editor, so the loop
     * guard belongs here, at the single dispatch point — not in each handler.
     * Warns at most once per window.
     */
    private overBudget(): boolean {
        const now = Date.now();
        const cutoff = now - ExtensionHostChannel.CALL_WINDOW_MS;
        this.callTimes = this.callTimes.filter((t) => t > cutoff);
        if (this.callTimes.length < ExtensionHostChannel.CALL_LIMIT) {
            this.callTimes.push(now);
            return false;
        }
        if (now - this.lastRateWarnAt >= ExtensionHostChannel.CALL_WINDOW_MS) {
            this.lastRateWarnAt = now;
            console.warn(
                `[extension-host] ${this.opts.extensionName}: exceeded ` +
                `${ExtensionHostChannel.CALL_LIMIT} capability calls per ` +
                `${ExtensionHostChannel.CALL_WINDOW_MS}ms — throttled.`
            );
        }
        return true;
    }

    private post(msg: HostMessage) {
        if (this.destroyed) return;
        // Nothing goes into the frame until its document has proven itself —
        // not even the handshake request's counterpart. Everything else is host
        // data: `init` (extension config + grants + context), pushed events such
        // as editor content, and call responses (a legit client queues its
        // calls until `init`, so nothing legitimate is in flight while armed is
        // false).
        if (!this.armed && msg.type !== 'exthello-request') return;
        // Round-trip through JSON to strip Svelte $state proxies (not cloneable).
        let plain: HostMessage;
        try {
            plain = JSON.parse(JSON.stringify(msg));
        } catch {
            return;
        }
        // targetOrigin MUST stay '*': the frame is sandboxed without
        // allow-same-origin, and an opaque-origin frame accepts messages only
        // when targetOrigin is a wildcard — both 'null' and the host's own
        // origin are silently dropped by Chromium (measured; see
        // ExtensionHostChannel.onMessage). So the origin cannot constrain the
        // destination, and a frame that navigated would receive anything posted
        // here — which is why arming (not targetOrigin) gates what we send.
        this.iframe.contentWindow?.postMessage(plain, '*');
    }

    private async onMessage(ev: MessageEvent) {
        if (this.destroyed) return;
        // Only accept messages from OUR sandboxed iframe: the exact window AND
        // an opaque origin ('null').
        //
        // NOTE: these two checks are necessary but NOT sufficient — sandbox
        // flags belong to the browsing context, so a document the frame
        // navigated to is still an opaque origin in the same window. Document
        // identity is what the handshake below establishes.
        if (ev.source !== this.iframe.contentWindow) return;
        if (ev.origin !== 'null') return;

        const data = ev.data;
        if (!data || typeof data !== 'object') return;

        if (data.type === 'exthello') {
            this.onHello(data as HostHello);
            return;
        }
        if (data.type !== 'call') return;

        const req = data as HostRequest;
        const id = req.id;
        if (typeof id !== 'string' || !id) return;

        // Identity check: no capability work, and no reply at all, for an
        // unproven document (a silent channel leaks nothing about itself).
        if (!this.armed) {
            console.warn(
                `[extension-host] ${this.opts.extensionName}: ignored a capability call from an ` +
                `un-handshaked document in the extension frame.`
            );
            return;
        }

        // Budget check: one extension must not starve the app (or the server
        // behind its capabilities) with a call loop.
        if (this.overBudget()) {
            this.post({ type: 'response', id, ok: false, error: 'rate limit exceeded' });
            return;
        }

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
