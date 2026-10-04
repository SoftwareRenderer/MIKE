// Live check of the extension-declared language layer against a running dev server:
//   MIKE_CHECK_PROJECT=<scratch project> MIKE_PAIR_CODE=<pairing code> node tests/declared-languages-check.mjs
//
// The probe WRITES fixture files into the named project's directory — the name is required because
// there is no safe guess — and removes them again. Like the other checks in this folder it assumes
// it runs on the same machine as the server.
//
// Covers, in the order the layers fail: per-file didOpen languageId, highlighting from the declared
// profile, block and region folding, Ctrl-/, and the visible symptoms of a wrong languageId.
import { chromium } from '@playwright/test';
import { attachVirtualAuthenticator, pairBrowser, pairingCodeFromEnv } from './helpers-auth.mjs';
import { writeFileSync, rmSync } from 'node:fs';

const BASE = 'https://localhost:3000';
const CODE = pairingCodeFromEnv();
const PROJECT = process.env.MIKE_CHECK_PROJECT;
const FOLD_KEY = 'Control+Shift+BracketLeft';    // foldKeymap: foldCode (toggle at cursor)
const UNFOLD_KEY = 'Control+Shift+BracketRight'; // foldKeymap: unfoldCode
const UNFOLD_ALL = 'Control+Alt+BracketRight';   // foldKeymap: unfoldAll

// The four ids are what tsserver turns into ScriptKinds; one extension owns all four files.
const WIRE_FIXTURES = {
  'wire-check.ts': 'export interface Box { x: number }\nconst b: Box = { x: 1 };\nexport { b };\n',
  'wire-check.tsx': 'export const A = () => <div>hi</div>;\n',
  'wire-check.js': 'export const f = (x) => x;\n',
  'wire-check.jsx': 'export const B = () => <span>yo</span>;\n',
};
const EXPECTED_WIRE_ID = {
  'wire-check.ts': 'typescript',
  'wire-check.tsx': 'typescriptreact',
  'wire-check.js': 'javascript',
  'wire-check.jsx': 'javascriptreact',
};

// The fold checks address lines by number, so the line map stays next to the source:
// 1 region marker, 2 opens a block closed on 5, 8 a brace inside a string, 9 inside a comment.
const SOURCE = `//#region shapes
export interface Box {
  x: number;
  y: number;
}
//#endregion
export function pick(s: string): string {
  const braceInString = "{";
  // a brace in a comment {
  return braceInString;
}
`;
const LINE = {
  region: 1,
  block: 2,
  braceInString: 8,
  braceInComment: 9,
  return: 10,
};

let failures = 0;
function check(name, ok, detail = '') {
  console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}${detail ? ' — ' + detail : ''}`);
  if (!ok) failures++;
}

const browser = await chromium.launch();
// The lane's certificate comes from the CA the server made in its own data dir; this probe measures
// the editor, not the certificate.
const ctx = await browser.newContext({ baseURL: BASE, ignoreHTTPSErrors: true });

// Record every didOpen that crosses a WebSocket, installed before any app code runs.
await ctx.addInitScript(() => {
  const send = WebSocket.prototype.send;
  WebSocket.prototype.send = function (data) {
    try {
      const text = typeof data === 'string' ? data : '';
      if (text.includes('textDocument/didOpen')) {
        const msg = JSON.parse(text);
        const inner = typeof msg?.data === 'string' ? JSON.parse(msg.data) : msg;
        const p = inner?.params?.textDocument || inner?.method?.params?.textDocument;
        if (p?.uri) {
          window.__didOpen = window.__didOpen || [];
          window.__didOpen.push({ uri: p.uri, languageId: p.languageId });
        }
      }
    } catch { /* not JSON-RPC we care about */ }
    return send.call(this, data);
  };
});
const page = await ctx.newPage();

// Sign in the way a person does: a virtual passkey, spent from a pairing code.
await attachVirtualAuthenticator(ctx, page);
await pairBrowser(page, BASE, CODE);
if (!PROJECT) {
  console.log('FAIL  set MIKE_CHECK_PROJECT to the name of a scratch project — fixtures are written into it');
  await browser.close();
  process.exit(1);
}
const listed = await (await page.request.get('/api/projects')).json();
const projects = Array.isArray(listed) ? listed : listed.projects || [];
const project = projects.find((p) => p.name === PROJECT || p.id === PROJECT);
if (!project?.path) {
  console.log(`FAIL  no project "${PROJECT}" (known: ${projects.map((p) => p.name).join(', ')})`);
  await browser.close();
  process.exit(1);
}

const fixtures = { ...WIRE_FIXTURES, 'declared-check.ts': SOURCE };
for (const [name, text] of Object.entries(fixtures)) writeFileSync(`${project.path}/${name}`, text);

await page.goto('/');
await page.waitForSelector('.project-item', { timeout: 15000 });
await page.locator('.project-item', { hasText: project.name }).first().click();
await page.waitForSelector('.top-bar', { timeout: 10000 });
await page.locator('.explorer-btn').click();

async function openFile(name) {
  await page.locator('.file-browser-item', { hasText: name }).first().click();
  await page.waitForSelector('.cm-editor', { timeout: 20000 });
}

// ── 1. the wire languageId, per file type ────────────────────────────────────
for (const [name, expected] of Object.entries(EXPECTED_WIRE_ID)) {
  await openFile(name);
  // Connecting, initializing and only then announcing the document takes a moment.
  let opens = [];
  for (let i = 0; i < 20; i++) {
    await page.waitForTimeout(1000);
    opens = await page.evaluate(() => window.__didOpen || []);
    if (opens.some((o) => o.uri.endsWith(name))) break;
  }
  const mine = opens.filter((o) => o.uri.endsWith(name));
  check(`${name} → didOpen languageId "${expected}"`,
    mine.length > 0 && mine[mine.length - 1].languageId === expected,
    mine.length ? JSON.stringify(mine.map((o) => o.languageId)) : 'no didOpen reached the wire');
}

// ── 2-4. what the declared profile does to the editor ────────────────────────
await openFile('declared-check.ts');
await page.waitForTimeout(2000);

const tagged = await page.evaluate(() => {
  const out = {};
  document.querySelectorAll('.cm-content .cm-line span').forEach((s) => {
    const cls = s.className || '(none)';
    (out[cls] = out[cls] || []).push(s.textContent);
  });
  return out;
});
const flat = Object.values(tagged).flat();
check('keywords coloured from the declared profile',
  flat.includes('interface') && flat.includes('export'), JSON.stringify(flat.slice(0, 6)));
check('the brace inside a string is string content',
  Object.values(tagged).some((t) => t.includes('"{"')));
check('the brace inside a comment is comment content',
  Object.values(tagged).some((t) => t.some((x) => (x || '').startsWith('// a brace in a comment'))));

const lineCount = () => page.locator('.cm-content .cm-line').count();
const placeholders = () => page.locator('.cm-foldPlaceholder').count();
async function cursorToLine(n) {
  await page.locator('.cm-content .cm-line').nth(n - 1).click({ position: { x: 120, y: 4 } });
}
async function unfold() {
  await page.keyboard.press(UNFOLD_KEY);
  await page.waitForTimeout(300);
  await page.keyboard.press(UNFOLD_ALL);
  await page.waitForTimeout(300);
}
async function foldLine(n, name) {
  const before = await lineCount();
  await cursorToLine(n);
  await page.keyboard.press(FOLD_KEY);
  await page.waitForTimeout(500);
  const after = await lineCount();
  const marks = await placeholders();
  check(name, marks > 0 && after < before, `lines ${before} → ${after}, placeholders ${marks}`);
  await unfold();
  check('unfold restores every line', (await lineCount()) === before && (await placeholders()) === 0);
  return before;
}

await foldLine(LINE.region, 'the region marker folds its block (a comment, so a marker not prose)');
const lines0 = await foldLine(LINE.block, 'a block the cursor is inside folds to its closer');

for (const [line, name] of [
  [LINE.braceInString, 'a brace inside a string opens no fold'],
  [LINE.braceInComment, 'a brace inside a comment opens no fold'],
]) {
  await cursorToLine(line);
  await page.keyboard.press(FOLD_KEY);
  await page.waitForTimeout(500);
  check(name, (await placeholders()) === 0 && (await lineCount()) === lines0,
    `lines ${await lineCount()}/${lines0}, placeholders ${await placeholders()}`);
}

await cursorToLine(LINE.return);
await page.keyboard.press('Control+/');
await page.waitForTimeout(400);
const edited = await page.locator('.cm-content .cm-line').nth(LINE.return - 1).textContent();
check('Ctrl-/ inserts the declared line comment', (edited || '').trimStart().startsWith('//'), JSON.stringify(edited));
await page.keyboard.press('Control+Z');
await page.waitForTimeout(300);

// ── 5. the symptoms of a wrong languageId ───────────────────────────────────
const body = await page.textContent('body');
for (const phrase of ['only be used in TypeScript files', "',' expected", 'Unknown language']) {
  check(`no "${phrase}" anywhere in the UI`, !body.includes(phrase));
}

for (const name of Object.keys(fixtures)) rmSync(`${project.path}/${name}`, { force: true });
await browser.close();
console.log(failures ? `\n${failures} check(s) failed` : '\nall checks passed');
process.exit(failures ? 1 : 0);
