// The frame's own script: plain JavaScript, no framework, no import of the host's code.
// window.extHost is what host-client.js put there — the only door to the host. Wait for
// ready(): the host sends the extension's config and its granted permissions with the
// init message, and a call made before that is queued, not answered.
(() => {
    'use strict';

    const host = window.extHost;

    host.ready().then(() => {
        const config = host.config || {};
        const suffix = config.suffix === undefined ? ' words' : String(config.suffix);
        const words = document.body.textContent.trim().split(/\s+/).filter(Boolean).length;
        document.body.textContent = words + suffix;
        return host.setSize(24);
    }).catch((err) => {
        host.error(err.message || String(err));
    });
})();
