/**
 * Diagnostic: verify the sandbox memory bar visually.
 *
 * Runs against a live dev server (see /tmp/kanban-debug-mem.log) with the mock
 * LLM on port 9998. Starts a real sandbox task (sleep probe), opens the
 * Sandbox panel, and screenshots the memory bar in both the running state
 * (fill to current usage + red peak tick) and the stopped state (empty fill,
 * tick persists).
 *
 * Usage:
 *   1. mock LLM:  setsid node frontend/tests/mock-llm.js 9998 &
 *   2. server:    setsid env MIKE_DATA_DIR=/tmp/kanban-debug-mem-data MIKE_PORT=3457 \
 *                  MIKE_ENCRYPTION_KEY=debug MIKE_LOGIN_PASSWORD=test ./kanban --no-tls &
 *   3. node frontend/tests/sandbox-memory-bar.mjs
 */
import { chromium } from '@playwright/test';

const BASE = 'http://localhost:3457';
const MOCK_URL = 'http://localhost:9998/v1';
const MEMORY_LIMIT_MB = 2048;
const SLEEP_PROBE = 'sandbox-sleep-probe';

async function api(page, url, options = {}) {
  const session = await page.evaluate(async (u) => {
    const res = await fetch(u, { credentials: 'include' });
    return res.ok ? res.json() : null;
  }, `${BASE}/api/auth/session`);
  const csrf = session?.csrf_token;
  return page.evaluate(async ({ apiUrl, opts, csrfToken }) => {
    const res = await fetch(apiUrl, {
      method: opts.method || 'GET',
      headers: {
        'Content-Type': 'application/json',
        ...(csrfToken ? { 'X-CSRF-Token': csrfToken } : {}),
      },
      credentials: 'include',
      body: opts.body ? JSON.stringify(opts.body) : undefined,
    });
    const data = await res.json().catch(() => null);
    return { ok: res.ok, status: res.status, data };
  }, { apiUrl: url, opts: options, csrfToken: csrf });
}

function fail(msg) {
  console.error(`FAIL: ${msg}`);
  process.exit(1);
}

const browser = await chromium.launch();
const page = await browser.newPage({ viewport: { width: 1280, height: 900 } });
page.on('console', (m) => { if (m.type() === 'error') console.log('[page error]', m.text()); });

try {
  // Login
  await page.goto(`${BASE}/`);
  const login = await api(page, `${BASE}/api/auth/login`, { method: 'POST', body: { password: 'test' } });
  if (!login.ok) fail(`login failed (${login.status})`);

  // Point the mock provider at the local mock LLM
  const config = await api(page, `${BASE}/api/config`);
  await api(page, `${BASE}/api/config`, {
    method: 'PUT',
    body: {
      ...config.data,
      providers: [{
        name: 'mock',
        display_name: 'Mock LLM',
        url: MOCK_URL,
        models: [{ name: 'mock-model', reasoning_effort: 'off' }],
        concurrency: 16,
      }],
      worker_provider: 'mock',
      worker_model: 'mock-model',
      utility_provider: 'mock',
      utility_model: 'mock-model',
    },
  });

  // Sandbox-enabled project on the shared test repo, 2048 MB limit
  const project = await api(page, `${BASE}/api/projects`, { method: 'POST', body: { name: 'mem-bar-debug', path: '/tmp/kanban-test-repo' } });
  if (!project.ok) fail(`project create failed: ${JSON.stringify(project.data)}`);
  const pid = project.data.id;
  const cfg = await api(page, `${BASE}/api/projects/${pid}`, {
    method: 'PUT',
    body: { sandbox_enabled: true, sandbox_memory_limit: MEMORY_LIMIT_MB },
  });
  if (!cfg.ok) fail(`project config failed: ${JSON.stringify(cfg.data)}`);

  // Start a sleep-probe task (routes the mock LLM to a 20s in-sandbox bash call)
  const task = await api(page, `${BASE}/api/projects/${pid}/tasks`, {
    method: 'POST',
    body: { prompt: `${SLEEP_PROBE}-${Date.now()}: run the probe command`, llama_server_url: MOCK_URL, model_name: 'mock-model' },
  });
  if (!task.ok) fail(`task create failed: ${JSON.stringify(task.data)}`);
  const tid = task.data.id;
  const start = await api(page, `${BASE}/api/projects/${pid}/tasks/${tid}/start`, { method: 'POST' });
  if (!start.ok) fail(`task start failed: ${JSON.stringify(start.data)}`);

  // Open the task view and wait for the sandbox bash tool call
  await page.evaluate((u) => { window.location.href = u; }, `/?project=${pid}&task=${tid}`);
  await page.waitForSelector('.task-detail-page', { timeout: 15000 });
  await page.waitForSelector('.tool-call-card', { timeout: 90000 });
  console.log('sandbox bash tool call running');

  // Open the sandbox panel
  await page.click('.sandbox-btn');
  await page.waitForSelector('.memory-value', { timeout: 30000 });

  // Wait for the fill to grow past 0 and the peak tick to appear
  await page.waitForFunction(() => {
    const fill = document.querySelector('.memory-bar-fill');
    const tick = document.querySelector('.memory-bar-peak');
    const w = fill ? parseFloat(fill.style.width) : 0;
    return w > 0 && tick !== null;
  }, { timeout: 30000 });

  const running = await page.evaluate(() => {
    const fill = document.querySelector('.memory-bar-fill');
    const tick = document.querySelector('.memory-bar-peak');
    const cs = (el) => getComputedStyle(el);
    return {
      value: document.querySelector('.memory-value')?.textContent,
      fillWidth: fill.style.width,
      fillColor: cs(fill).backgroundColor,
      tickLeft: tick.style.left,
      tickColor: cs(tick).backgroundColor,
      tickTitle: tick.title,
      peakText: document.querySelector('.memory-peak')?.textContent,
    };
  });
  console.log('RUNNING STATE:', JSON.stringify(running, null, 2));
  if (parseFloat(running.fillWidth) <= 0) fail('fill width is 0 while running');
  if (running.fillColor === 'rgba(0, 0, 0, 0)') fail('fill is transparent');
  if (running.tickColor !== 'rgb(248, 81, 73)') fail(`tick is not status red: ${running.tickColor}`);
  if (parseFloat(running.tickLeft) <= 0) fail('tick position is 0');

  const section = page.locator('.memory-section');
  await section.screenshot({ path: '/tmp/sandbox-memory-bar-running.png' });

  // Stop the task — the client keeps the last real sample, so the bar with
  // fill and peak tick must stay visible (the not-running + limit-known state
  // is covered by unit tests; the server's empty sample carries limit 0).
  await page.click('.overlay', { position: { x: 10, y: 10 } });
  const stop = await api(page, `${BASE}/api/projects/${pid}/tasks/${tid}/stop`, { method: 'POST' });
  if (!stop.ok) fail(`task stop failed: ${JSON.stringify(stop.data)}`);
  await page.waitForTimeout(5000); // let the sampler settle (2s interval)
  await page.click('.sandbox-btn');
  await page.waitForSelector('.memory-value', { timeout: 30000 });

  const stopped = await page.evaluate(() => {
    const fill = document.querySelector('.memory-bar-fill');
    const tick = document.querySelector('.memory-bar-peak');
    return {
      value: document.querySelector('.memory-value')?.textContent,
      fillWidth: fill?.style.width ?? 'NO BAR',
      tickLeft: tick?.style.left ?? 'NO TICK',
    };
  });
  console.log('STOPPED STATE:', JSON.stringify(stopped, null, 2));
  if (stopped.fillWidth === 'NO BAR') fail('bar disappeared after stop');
  if (parseFloat(stopped.fillWidth) <= 0) fail(`expected persisted fill after stop, got ${stopped.fillWidth}`);
  if (stopped.tickLeft === 'NO TICK') fail('peak tick disappeared after stop');

  await page.locator('.memory-section').screenshot({ path: '/tmp/sandbox-memory-bar-stopped.png' });
  console.log('PASS: screenshots at /tmp/sandbox-memory-bar-running.png and /tmp/sandbox-memory-bar-stopped.png');
} finally {
  await browser.close();
}
