// Ad-hoc browser probe (throwaway): renders both sandbox modals and reports the computed style of
// every shared class, so the refactor can be checked against the real browser cascade.
import { chromium } from 'playwright';

const BASE = 'http://localhost:3000';
const LIGHT = '/home/sandbox/MIKE-extensions/css/mike-light/styles.css';

const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
page.on('pageerror', (e) => console.log('PAGEERROR', String(e)));
page.on('console', (m) => { if (m.type() === 'error') console.log('CONSOLE-ERROR', m.text().slice(0, 160)); });

await page.goto(BASE, { waitUntil: 'networkidle' });
// Login
await page.locator('input[type="password"]').fill('test');
await page.locator('button[type="submit"], .login-btn').first().click();
await page.waitForSelector('.project-item, .board', { timeout: 15000 });

const csrf = await page.evaluate(async () => (await (await fetch('/api/auth/session')).json()).csrf_token || '');
const H = { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf };

const api = async (method, url, body) => page.evaluate(async ([m, u, b, h]) => {
    const r = await fetch(u, { method: m, headers: h, body: b ? JSON.stringify(b) : undefined });
    return { ok: r.ok, status: r.status, json: await r.json().catch(() => null) };
}, [method, url, body, H]);

let projects = (await api('GET', '/api/projects')).json;
if (!projects?.length) {
    console.log('no project; creating one');
    const { execSync } = await import('child_process');
    execSync('rm -rf /tmp/visual-repo && mkdir -p /tmp/visual-repo && git -C /tmp/visual-repo init -q && echo hi > /tmp/visual-repo/a && git -C /tmp/visual-repo add -A && git -C /tmp/visual-repo -c user.email=t@t -c user.name=t commit -qm init');
    const made = await api('POST', '/api/projects', { name: 'visual-probe', path: '/tmp/visual-repo' });
    console.log('create', made.status, JSON.stringify(made.json)?.slice(0, 120));
    projects = (await api('GET', '/api/projects')).json;
}
const project = projects[0];
console.log('project', project.id, project.name);

const put = await api('PUT', `/api/projects/${project.id}`, {
    sandbox_enabled: true,
    sandbox_network_isolation: false,
    sandbox_memory_limit: 1024,
    sandbox_image: 'docker.io/library/ubuntu:latest',
    sandbox_mounts: [
        { host_path: '/home/user/a/very/long/host/path/that/should/truncate/in/narrow/rows', container_path: '/mnt/data', mode: 'rw' },
        { host_path: '/etc/config', container_path: '/etc/config', mode: 'ro' },
    ],
    sandbox_envs: [{ key: 'UI_TEST_KEY', value: 'ui_value' }, { key: 'EMPTY_VALUE', value: '' }],
    sandbox_capabilities: ['CAP_NET_RAW', 'CAP_SYS_ADMIN', 'CAP_CHECKPOINT_RESTORE'],
    sandbox_forward_ports: [{ container_port: 3000 }, { container_port: 8080, host_ip: '127.0.0.1' }],
});
console.log('PUT sandbox settings', put.status);

// The read-only sections need the task view; the editable modal needs the board view.
async function modalShot(tag) {
    await page.reload({ waitUntil: 'networkidle' });
    await page.waitForSelector('.project-item', { timeout: 10000 });
    await page.click('.project-item');
    await page.waitForSelector('.sandbox-config-btn', { timeout: 10000 });
    await page.click('.sandbox-config-btn');
    await page.waitForSelector('[role="dialog"]', { timeout: 10000 });
    await page.waitForTimeout(900);
    await page.screenshot({ path: `/tmp/vis-modal-${tag}.png` });
    console.log(`modal ${tag}:`, JSON.stringify(await probe(page), null, 0));
    await page.keyboard.press('Escape');
    await page.locator('[role="dialog"]').waitFor({ state: 'detached', timeout: 5000 }).catch(() => {});
}

async function probe(p) {
    return p.evaluate(() => {
        const want = ['.sandbox-row', '.sandbox-label', '.sandbox-value', '.sandbox-value-mono', '.sandbox-section-label',
            '.sandbox-item', '.sandbox-item-key', '.sandbox-item-text', '.sandbox-eq', '.sandbox-mode', '.sandbox-cap',
            '.config-label', '.config-desc', '.port-input', '.mount-input', '.env-input', '.btn-add-mount',
            '.btn-remove-mount', '.port-hint', '.mount-arrow', '.radio-label'];
        const out = {};
        for (const sel of want) {
            const el = document.querySelector(sel);
            if (!el) { out[sel] = 'ABSENT'; continue; }
            const cs = getComputedStyle(el);
            const r = el.getBoundingClientRect();
            out[sel] = {
                color: cs.color, bg: cs.backgroundColor, fs: cs.fontSize, fw: cs.fontWeight,
                pad: cs.padding, border: cs.borderBottomWidth + ' ' + cs.borderBottomColor,
                radius: cs.borderTopLeftRadius, w: Math.round(r.width), h: Math.round(r.height),
                gap: cs.gap, trunc: cs.textOverflow,
            };
        }
        const dlg = document.querySelector('[role="dialog"]');
        out['__dialog'] = dlg ? { h: Math.round(dlg.getBoundingClientRect().height) } : 'ABSENT';
        return out;
    });
}

await modalShot('dark');

// Light palette: the pack is not installed, so load its sheet the way the injector does
// (a global stylesheet appended to <head>, after the app CSS).
const tag = await page.addStyleTag({ path: LIGHT });
await modalShot('light');
await tag.evaluate((n) => n.remove());

// SandboxPanel (read-only): open a task and press the sandbox button
await page.reload({ waitUntil: 'networkidle' });
await page.waitForSelector('.project-item', { timeout: 10000 });
await page.click('.project-item');
const cards = await page.locator('.card').count();
console.log('cards', cards);
if (cards) {
    await page.locator('.card').first().click();
    await page.waitForSelector('.sandbox-btn', { timeout: 10000 });
    await page.click('.sandbox-btn');
    await page.waitForSelector('.panel-body', { timeout: 10000 });
    await page.waitForTimeout(1200);
    await page.screenshot({ path: '/tmp/vis-panel-dark.png' });
    console.log('panel dark:', JSON.stringify(await probe(page), null, 0));
    const t2 = await page.addStyleTag({ path: LIGHT });
    await page.waitForTimeout(300);
    await page.screenshot({ path: '/tmp/vis-panel-light.png' });
    console.log('panel light:', JSON.stringify(await probe(page), null, 0));
    await t2.evaluate((n) => n.remove());
}

await browser.close();
