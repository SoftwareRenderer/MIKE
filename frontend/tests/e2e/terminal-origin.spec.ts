import { test, expect } from '@playwright/test';
import { getProjectId } from './helpers';

// The terminal endpoint hands out a shell, so its WebSocket handshake verifies
// the browser Origin (same host) rather than opting out with
// InsecureSkipVerify — the posture the LSP endpoint has always kept. The refusal a
// cross-site Origin gets is pinned by `backend/httputil/ws_origin_test.go` and
// `TestHandleTerminal_OriginVerification`; what needs the browser is that a page of the
// app's own still gets a working shell out of that handshake.

test.describe.serial('Terminal WebSocket origin verification', () => {
    test('a same-host page connects and gets a working shell', async ({ page }) => {
        await page.goto('/');
        const projectId = await getProjectId(page);
        const marker = `MIKE_TERM_${Date.now()}`;

        // Opened from the page context, so the browser attaches its own
        // same-host Origin exactly like the terminal panel does. A rejected
        // handshake fires onerror without ever opening.
        const output = await page.evaluate(
            async ({ pid, token }) =>
                new Promise<string>((resolve, reject) => {
                    const proto = window.location.protocol === 'https:' ? 'wss' : 'ws';
                    const ws = new WebSocket(`${proto}://${window.location.host}/api/projects/${pid}/terminal`);
                    let collected = '';
                    const timer = setTimeout(() => {
                        ws.close();
                        resolve(collected);
                    }, 20000);
                    ws.onmessage = (ev) => {
                        collected += typeof ev.data === 'string' ? ev.data : '';
                        if (collected.includes(token)) {
                            clearTimeout(timer);
                            ws.close();
                            resolve(collected);
                        }
                    };
                    ws.onerror = () => {
                        clearTimeout(timer);
                        reject(new Error('terminal websocket handshake was rejected'));
                    };
                    ws.onopen = () => {
                        ws.send(JSON.stringify({ type: 'input', data: `echo ${token}\n` }));
                    };
                }),
            { pid: projectId, token: marker },
        );

        expect(output, 'the shell never echoed the marker back').toContain(marker);
    });

});
