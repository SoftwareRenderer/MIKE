// Diagnostic: verify that sticky-scroll section headers are RE-EXTRACTED after
// an external (on-disk) file change is reconciled into the file editor.
//
// Scenario (project-mode explorer, no task/LLM needed):
//   1. Open sample.js (4 functions, 104 lines, gamma FIRST) in the editor.
//      At the top of the file the sticky bar shows `function gamma() {`.
//   2. Rewrite the file on disk: prepend 5 comment lines + rename gamma ->
//      gamma_renamed (declaration moves 1 -> 6).
//   3. fs_event -> checkExternalChange() -> applyDiskContent() replaces the
//      doc WITHOUT setting a selection. The fix schedules the same debounced
//      re-extraction the selection path uses; verify via state that survives
//      this headless env's frozen CM viewport (IntersectionObserver quirk):
//        a) symbol outline (fed by onSymbolsUpdate) now lists gamma_renamed;
//        b) the sticky plugin's currentHeader flips from the stale
//           {line:1, endLine:26} to null — topLine 1 is above the new first
//           header (line 6); the stale line-1 header would still match and
//           keep the old `function gamma() {` on the bar.
//      Bonus: when CM's viewport does track scroll, the sticky bar itself
//      shows `function gamma_renamed() {`.
//
// Run: make run (dev server, port 3000, DANGER_ZONE=1) then:
//      node frontend/tests/diag-sticky-external.mjs
import http from 'node:http';
import { mkdirSync, writeFileSync, rmSync } from 'node:fs';
import { execSync } from 'node:child_process';
import { join } from 'path';
import { chromium } from 'playwright';

const BASE = 'http://localhost:3000';
const REPO = '/tmp/kanban-sticky-sanity';
const FILE = 'sample.js';
const PROJECT_NAME = 'sticky-sanity-project';

// 4 functions, each 25 lines, one blank line between. gamma FIRST so its
// sticky header shows at the top of the file (no scroll needed):
// gamma 1-25, 26 blank, alpha 27-51, 52 blank, beta 53-77, 78 blank,
// delta 79-103, 104 blank.
function fn(name, tag) {
  const lines = [`function ${name}() {`];
  for (let i = 1; i <= 23; i++) lines.push(`  const ${tag}${i} = ${i};`);
  lines.push('}');
  return lines.join('\n');
}
const ORIGINAL = [fn('gamma', 'g'), fn('alpha', 'a'), fn('beta', 'b'), fn('delta', 'd')]
  .join('\n\n') + '\n';
// Disk mutation: prepend 5 lines + rename gamma (decl moves 1 -> 6).
const PREFIX = ['// prepended-1', '// prepended-2', '// prepended-3', '// prepended-4', '// prepended-5']
  .join('\n') + '\n';
const MODIFIED = PREFIX + ORIGINAL.replace('function gamma', 'function gamma_renamed');

// --- API helpers (cookie + CSRF) -------------------------------------------
function request(method, path, body, cookie, csrf) {
  return new Promise((resolve, reject) => {
    const data = body ? JSON.stringify(body) : null;
    const req = http.request(BASE + path, {
      method,
      headers: {
        'Content-Type': 'application/json',
        ...(cookie ? { Cookie: cookie } : {}),
        ...(csrf && method !== 'GET' ? { 'X-CSRF-Token': csrf } : {}),
        ...(data ? { 'Content-Length': Buffer.byteLength(data) } : {}),
      },
    }, (res) => {
      let buf = '';
      res.on('data', (d) => (buf += d));
      res.on('end', () => {
        let json = null;
        try { json = buf ? JSON.parse(buf) : null; } catch { /* not json */ }
        resolve({ status: res.statusCode, json, setCookie: res.headers['set-cookie'] });
      });
    });
    req.on('error', reject);
    if (data) req.write(data);
    req.end();
  });
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function main() {
  // --- repo + project ---
  try { rmSync(REPO, { recursive: true, force: true }); } catch { /* ignore */ }
  mkdirSync(REPO, { recursive: true });
  writeFileSync(join(REPO, FILE), ORIGINAL);
  execSync('git init -q && git config user.email t@t.local && git config user.name T && git add -A && git commit -qm init', { cwd: REPO });

  const login = await request('POST', '/api/auth/login', { password: 'test' });
  if (login.status !== 200) throw new Error(`login failed: ${login.status}`);
  const cookie = (login.setCookie || []).map((c) => c.split(';')[0]).find((c) => c.startsWith('PHPSESSID='));
  const session = await request('GET', '/api/auth/session', null, cookie);
  const csrf = session.json.csrf_token;
  if (!csrf) throw new Error(`no csrf: ${JSON.stringify(session.json)}`);

  const list = (await request('GET', '/api/projects', null, cookie)).json;
  const projects = Array.isArray(list) ? list : list.projects || [];
  const existing = projects.find((p) => p.name === PROJECT_NAME);
  if (existing) await request('DELETE', `/api/projects/${existing.id}`, null, cookie, csrf);
  const created = (await request('POST', '/api/projects', { name: PROJECT_NAME, path: REPO }, cookie, csrf)).json;
  if (!created.id) throw new Error(`create project: ${JSON.stringify(created)}`);
  const projectId = created.id;
  console.log(`project created: ${projectId}`);

  // --- browser ---
  const browser = await chromium.launch();
  const ctx = await browser.newContext({ viewport: { width: 1400, height: 900 }, extraHTTPHeaders: { Cookie: cookie } });
  const page = await ctx.newPage();
  page.on('console', (m) => { if (m.type() === 'error') console.log('[page error]', m.text().slice(0, 200)); });

  const stickyText = () => page.evaluate(() => {
    const el = document.querySelector('.cm-sticky-scroll');
    return el && el.style.display !== 'none' ? el.textContent.trim() : '';
  });

  const pluginState = () => page.evaluate(() => {
    const v = document.querySelector('.cm-editor')?.__view;
    if (!v) return { noView: true };

    const inst = v.plugins
      ? v.plugins.map((p) => p && p.value).find((p) => p && p.element && p.element.className === 'cm-sticky-scroll')
      : null;
    return {
      line1: v.state.doc.line(1).text,
      line6: v.state.doc.line(6).text,
      topLine: v.state.doc.lineAt(v.viewport.from).number,
      currentHeader: inst ? inst.currentHeader : 'no-plugin',
    };
  });

  const outlineText = () => page.evaluate(() =>
    (document.querySelector('.symbol-outline')?.textContent || '').trim());

  try {
    await page.goto(`${BASE}/`);
    await page.waitForSelector('.project-item', { timeout: 15000 });
    await page.locator('.project-item', { hasText: PROJECT_NAME }).click();
    await page.waitForSelector('.top-bar', { timeout: 15000 });
    await page.locator('.explorer-btn').click();
    await page.waitForSelector('.file-browser-list', { timeout: 15000 });
    await page.locator('.file-browser-item', { hasText: FILE }).click();
    await page.waitForSelector('.cm-content', { timeout: 15000 });
    console.log('editor open');
    await sleep(1500); // let initial extractSymbols (500ms) + sticky settle

    // 1. At the top of the file the sticky bar shows gamma's header and the
    //    plugin has it as its current header.
    let before = await stickyText();
    for (let i = 0; i < 10 && !before.includes('function gamma()'); i++) {
      await sleep(400);
      before = await stickyText();
    }
    if (!before.includes('function gamma()')) {
      throw new Error(`gamma header not shown at top, sticky shows: ${JSON.stringify(before)}`);
    }
    const stateBefore = await pluginState();
    console.log(`sticky BEFORE: ${JSON.stringify(before)}`);
    console.log(`plugin BEFORE: ${JSON.stringify(stateBefore)}`);

    // 2. External change on disk: prepend 5 lines + rename gamma.
    writeFileSync(join(REPO, FILE), MODIFIED);
    console.log('file mutated on disk (fs_event should reconcile)');

    // 3. Wait for the editor doc to reconcile (content contains the rename).
    await page.waitForFunction(() =>
      document.querySelector('.cm-content')?.textContent.includes('gamma_renamed'), { timeout: 15000 });
    console.log('editor content reconciled');

    // 4. Let the fix's 300ms debounced re-extraction run, then inspect state.
    await sleep(700);
    const stateAfter = await pluginState();
    console.log(`plugin AFTER:  ${JSON.stringify(stateAfter)}`);
    const outlineAfter = await outlineText();
    console.log(`outline AFTER: ${JSON.stringify(outlineAfter.slice(0, 140))}`);

    // 5. Bonus visual check (works whenever CM's viewport tracks scroll,
    //    which it does in a real browser): bring line 6 to the viewport top.
    await page.evaluate(() => {
      const v = document.querySelector('.cm-editor')?.__view;
      v.dispatch({ effects: v.constructor.scrollIntoView(v.state.doc.line(6).from, { y: 'start', yMargin: 0 }) });
    });
    await sleep(800);
    const after = (await stickyText()).trim();
    console.log(`sticky AFTER:  ${JSON.stringify(after)}`);

    // --- verdict ---
    // The re-extraction happened if the plugin moved OFF the stale line-1
    // header: viewport topLine 1 -> new first header is line 6 -> no match ->
    // null; or (viewport tracked) header.line === 6 / sticky shows the name.
    const headerUpdated =
      stateAfter.currentHeader === null ||
      (stateAfter.currentHeader && stateAfter.currentHeader.line === 6) ||
      after.includes('gamma_renamed');
    const reextracted = stateAfter.line6 === 'function gamma_renamed() {'
      && headerUpdated
      && JSON.stringify(stateBefore.currentHeader) !== JSON.stringify(null);
    const outlineOk = outlineAfter.includes('gamma_renamed');
    if (reextracted && outlineOk) {
      console.log('PASS: headers + symbols re-extracted after external change' +
        (after.includes('gamma_renamed')
          ? ' (sticky bar shows the new header)'
          : ' (sticky hidden above the first section — correct at file top)'));
      process.exitCode = 0;
    } else {
      console.log(`FAIL: reextracted=${reextracted} outlineOk=${outlineOk}`);
      process.exitCode = 1;
    }
  } finally {
    await ctx.close();
    await browser.close();
    await request('DELETE', `/api/projects/${projectId}`, null, cookie, csrf);
  }
}

main().catch((e) => { console.error('ERROR:', e.message); process.exitCode = 1; });
