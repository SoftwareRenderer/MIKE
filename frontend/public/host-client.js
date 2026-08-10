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

    function call(cap, args) {
        return new Promise((resolve, reject) => {
            const id = uid();
            pending.set(id, { resolve, reject });
            post({ type: 'call', id, cap, args: args || {} });
        });
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

        if (msg.type === 'init') {
            granted = Array.isArray(msg.granted) ? msg.granted : [];
            config = msg.config;
            context = msg.context;
            initialized = true;
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

        log: (level, message) => call('log.write', { level: level || 'info', message: String(message || '') }),

        loading: (message) => call('ui.status', { state: 'loading', message: String(message || '') }),

        error: (message) => call('ui.status', { state: 'error', message: String(message || '') }),

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
})();
