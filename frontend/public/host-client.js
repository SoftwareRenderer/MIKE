/**
 * host-client.js — shared bridge for sandboxed UI extensions.
 *
 * Wraps the postMessage capability protocol into a small API. The extension
 * can only call capabilities granted by the host (manifest ∩ catalog).
 */
(() => {
    'use strict';

    if (window.__extHostReady) return; // idempotent
    window.__extHostReady = true;

    const pending = new Map();   // id -> { resolve, reject }
    const handlers = new Map();  // cap -> Set<handler>
    let idCounter = 0;
    let granted = [];
    let config = null;
    let context = null;
    let initResolvers = [];
    let initialized = false;
    let buffered = [];           // messages received before init (defensive)

    const uid = () => 'c' + Date.now().toString(36) + '-' + (++idCounter);

    const post = (msg) => window.parent.postMessage(msg, '*');

    // ── channel handshake (document identity) ───────────────────────
    // The host mints a one-time token per mount and puts it in this document's
    // URL as `extch`. Only the document the host actually mounted knows it, so
    // echoing it back is what opens the channel: until the host accepts it, no
    // `init` and no pushed events arrive, and calls are refused. Never send a
    // call before `init` — buffer it instead.
    const channelToken = (function () {
        try { return new URLSearchParams(window.location.search).get('extch') || ''; }
        catch (e) { return ''; }
    })();
    let callQueue = [];

    function sendHello() { post({ type: 'exthello', token: channelToken }); }

    function call(cap, args) {
        return new Promise((resolve, reject) => {
            const id = uid();
            pending.set(id, { resolve, reject });
            const msg = { type: 'call', id, cap, args: args || {} };
            if (initialized) post(msg); else callQueue.push(msg);
        });
    }

    function flushCalls() {
        const queued = callQueue;
        callQueue = [];
        for (const m of queued) post(m);
    }

    function handleMessage(msg) {
        if (msg.type === 'response') {
            const p = pending.get(msg.id);
            if (!p) return;
            pending.delete(msg.id);
            if (msg.ok) p.resolve(msg.data);
            else p.reject(new Error(msg.error || 'host call failed'));
        } else if (msg.type === 'event') {
            const set = handlers.get(msg.cap);
            if (!set) return;
            for (const h of set) {
                try { h(msg.data); } catch (e) { console.error('[ext] event handler error:', e); }
            }
        }
    }

    window.addEventListener('message', (ev) => {
        // Only accept messages from our embedding parent (the host app) —
        // never from sibling frames or any other window.
        if (ev.source !== window.parent) return;
        const msg = ev.data;
        if (!msg || typeof msg !== 'object') return;

        if (msg.type === 'exthello-request') {
            // The host is asking who is in this frame. Answer on every load —
            // a reload produces a new document that must re-handshake.
            sendHello();
            return;
        }

        if (msg.type === 'init') {
            granted = Array.isArray(msg.granted) ? msg.granted : [];
            config = msg.config;
            context = msg.context;
            initialized = true;
            // Outbound calls buffered until the channel opened.
            flushCalls();
            // Flush anything buffered before init.
            const flushed = buffered;
            buffered = [];
            for (const m of flushed) handleMessage(m);
            // Reassign to a fresh array (not mutate) — `resolvers` aliases the
            // old array, so mutating it would empty the list we iterate.
            const resolvers = initResolvers;
            initResolvers = [];
            for (const resolve of resolvers) resolve();
        } else {
            if (!initialized) {
                buffered.push(msg);
                return;
            }
            handleMessage(msg);
        }
    });

    const extHost = {
        ready() {
            if (initialized) return Promise.resolve();
            return new Promise((resolve) => initResolvers.push(resolve));
        },

        get granted() { return granted.slice(); },
        get config() { return config; },
        get context() { return context; },

        getConfig: () => call('config.get'),

        proxy: (url, method, body) => call('network.proxy', {
            url,
            method: method || 'GET',
            body: body || '',
        }),

        createTask: {
            description: {
                insertText: (text) => call('createTask.description.insertText', { text: String(text || '') }),
            },
        },

        editor: {
            addFooterButton: (id, label, active) => call('editor.addFooterButton', {
                id: String(id || ''),
                ...(label !== undefined ? { label: String(label) } : {}),
                active: !!active,
            }),
            removeFooterButton: (id) => call('editor.removeFooterButton', { id: String(id || '') }),
            getContent: () => call('editor.getContent', {}),
            setContent: (content) => call('editor.setContent', { content: String(content || '') }),
            setOverlay: (visible) => call('editor.setOverlay', { visible: !!visible }),
            save: () => call('editor.save', {}),
        },

        preview: {
            /**
             * Serve a doc; resolves to { token, url }; supersedes this editor's
             * previous doc. Scripts off always selects the no-script posture,
             * whatever `network` asks for.
             */
            serve: (content, scripts, network) => {
                const cap = !scripts ? 'preview.serve' : network ? 'preview.serveNetwork' : 'preview.serveScripts';
                if (!granted.includes(cap)) {
                    return Promise.reject(new Error(cap + ' is not granted to this extension'));
                }
                return call(cap, { content: String(content || '') });
            },
            /** Free a served doc early (TTL is the backstop). */
            release: (token) => call('preview.release', { token: String(token || '') }),
            /**
             * The token's last document navigation { seq, path, ok, isDoc }
             * — server-side truth for the watchdog's in-tree allow-list
             * decision (the frame's own URL is unreadable: opaque origin).
             * Rejects if the doc is gone (404) — treat as an escape.
             */
            navState: (token) => call('preview.navState', { token: String(token || '') }),
        },

        log: (level, message) => call('log.write', { level: level || 'info', message: String(message || '') }),

        loading: (message) => call('ui.status', { state: 'loading', message: String(message || '') }),

        error: (message) => call('ui.status', { state: 'error', message: String(message || '') }),

        /**
         * Host-rendered app-level modal (the extension's own iframe is
         * invisible/collapsed, so it can't render this itself). Resolves
         * when the user dismisses it.
         */
        modal: (opts) => call('ui.modal', {
            message: String((opts && opts.message) || ''),
            ...(opts && opts.title !== undefined ? { title: String(opts.title) } : {}),
            ...(opts && opts.confirmLabel !== undefined ? { confirmLabel: String(opts.confirmLabel) } : {}),
        }),

        hide: () => call('ui.status', { state: 'hidden' }),

        setSize: (height) => call('ui.setSize', { height: Number(height) || 0 }),

        storage: {
            get: (key) => call('storage.get', { key: String(key || '') }),
            set: (key, value) => call('storage.set', { key: String(key || ''), value }),
            delete: (key) => call('storage.delete', { key: String(key || '') }),
            list: (prefix) => call('storage.list', { prefix: prefix ? String(prefix) : '' }),
        },

        theme: {
            /** Read the host's current CSS custom properties (see theme.get). */
            get: () => call('theme.get', {}),
        },

        on(cap, handler) {
            if (typeof handler !== 'function') return () => {};
            if (!handlers.has(cap)) handlers.set(cap, new Set());
            handlers.get(cap).add(handler);
            return () => {
                const set = handlers.get(cap);
                if (set) {
                    set.delete(handler);
                    if (set.size === 0) handlers.delete(cap);
                }
            };
        },
    };

    window.extHost = extHost;

    // Announce ourselves immediately; the host also asks on each frame load.
    sendHello();
})();
