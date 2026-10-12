import { test, expect } from '@playwright/test';
import crypto from 'node:crypto';
import https from 'node:https';
import { getProjectId } from './helpers';

// The terminal endpoint hands out a shell, so its WebSocket handshake verifies
// the browser Origin (same host) rather than opting out with
// InsecureSkipVerify — the posture the LSP endpoint has always kept. These
// specs pin both sides: a same-host page from the app still gets a working
// shell, and a cross-site handshake is refused before any shell exists.

/**
 * Performs a raw WebSocket upgrade handshake and returns the HTTP status.
 *
 * Over TLS, never the plain port: a plaintext request is answered with the redirect to HTTPS before any
 * route runs, so every Origin — refused or accepted — would read 301 and the Origin check would go
 * unmeasured. The lane's leaf is signed by a CA its own data directory created, which nothing trusts;
 * the certificate is not what this spec measures, the same posture `use.ignoreHTTPSErrors` takes for
 * the browser.
 */
function upgradeStatus(baseURL: string, path: string, origin: string, cookie = ''): Promise<number> {
    const base = new URL(baseURL);
    return new Promise<number>((resolve, reject) => {
        const req = https.request(
            {
                host: base.hostname,
                port: base.port || 443,
                rejectUnauthorized: false,
                path,
                method: 'GET',
                headers: {
                    Connection: 'Upgrade',
                    Upgrade: 'websocket',
                    'Sec-WebSocket-Key': crypto.randomBytes(16).toString('base64'),
                    'Sec-WebSocket-Version': '13',
                    Origin: origin,
                    ...(cookie ? { Cookie: cookie } : {}),
                },
            },
            (res) => {
                res.resume();
                res.on('end', () => resolve(res.statusCode ?? 0));
            },
        );
        // A successful handshake answers 101 and Node surfaces it as 'upgrade'.
        req.on('upgrade', () => {
            req.destroy();
            resolve(101);
        });
        req.on('error', reject);
        req.end();
    });
}

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

    test('a cross-site origin is refused before any shell is created', async ({ page, context }) => {
        await page.goto('/');
        const projectId = await getProjectId(page);
        const baseURL = test.info().project.use.baseURL ?? 'https://localhost:3000';
        const path = `/api/projects/${projectId}/terminal`;
        const host = new URL(baseURL).host;
        // The session cookie rides along, so Origin is the only variable here.
        const cookie = (await context.cookies(baseURL))
            .map((c) => `${c.name}=${c.value}`)
            .join('; ');

        for (const origin of ['http://evil.example', 'https://evil.example', 'null']) {
            expect(await upgradeStatus(baseURL, path, origin, cookie), `origin ${origin} was not rejected`).toBe(403);
        }
        // The same-host origin still upgrades (the app's own page).
        expect(await upgradeStatus(baseURL, path, `http://${host}`, cookie)).toBe(101);
    });
});
