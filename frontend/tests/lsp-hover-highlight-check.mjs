// Live check that an LSP tooltip paints its signature block with syntax colours:
//   MIKE_CHECK_PROJECT=<project with go + ts servers> MIKE_PAIR_CODE=<pairing code> node tests/lsp-hover-highlight-check.mjs
//   # optional: MIKE_BASE=https://localhost:3000 (TLS server; certificate errors are ignored)
//
// Writes fixture files into the named project and removes them — the name is required because there is
// no safe guess. It answers what jsdom cannot: do the classes on a hover's code block actually paint,
// and do they follow the open file rather than the one the server connected for. Markup-level coverage
// is in src/lib/utils/__tests__/lsp-doc-highlight.unit.svelte.ts.
import { chromium } from '@playwright/test';
import { attachVirtualAuthenticator, pairBrowser, pairingCodeFromEnv } from './helpers-auth.mjs';
import { writeFileSync, rmSync } from 'node:fs';

const BASE = process.env.MIKE_BASE || 'http://localhost:3000';
const CODE = pairingCodeFromEnv();
const PROJECT = process.env.MIKE_CHECK_PROJECT;

// Hover targets per fixture. `func` is a keyword in the declared Go profile and not in TypeScript's,
// `function` the reverse: if the client kept answering with the first file's grammar, the TS signature
// would have no coloured `function`.
const CASES = [
  {
    file: 'mike-hover-check.go',
    source: 'package main\n\nimport "fmt"\n\nfunc main() {\n\tfmt.Println("hover me")\n}\n',
    token: 'Println',
    keyword: 'func',
  },
  {
    file: 'mike-hover-check.ts',
    source: 'export function addTwo(a: number): number {\n  return a + 2;\n}\n\nexport const twice = addTwo(1);\n',
    token: 'addTwo',
    keyword: 'function',
  },
];

let failures = 0;
function check(name, ok, detail = '') {
  console.log(`${ok ? 'PASS' : 'FAIL'}  ${name}${detail ? ' — ' + detail : ''}`);
  if (!ok) failures++;
}

const browser = await chromium.launch();
const ctx = await browser.newContext({ baseURL: BASE, ignoreHTTPSErrors: true });
const page = await ctx.newPage();

// Sign in the way a person does: a virtual passkey, spent from a pairing code.
try {
  await attachVirtualAuthenticator(ctx, page);
  await pairBrowser(page, BASE, CODE);
} catch (e) {
  console.log(`FAIL  pairing with ${BASE} — ${String(e).split('\n')[0]}`);
  await browser.close();
  process.exit(1);
}
if (!PROJECT) {
  console.log('FAIL  set MIKE_CHECK_PROJECT to a project whose Go and TypeScript servers run — fixtures are written into it');
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

// Without both servers installed and editor-enabled there is nothing to hover, and a hover that never
// opens would otherwise be read as a highlighting bug.
const exts = await (await page.request.get('/api/extensions')).json();
const installed = Array.isArray(exts) ? exts : exts.extensions || [];
for (const wanted of ['gopls', 'typescript-language-server']) {
  const ext = installed.find((e) => e.name === wanted);
  check(`${wanted} installed and editor-enabled`,
    !!ext?.enabled && ext?.manifest?.capabilities?.lsp?.editor_enabled !== false,
    ext ? `enabled=${ext.enabled}` : 'not installed');
}
if (failures) {
  console.log('\nthe fixtures need both language servers; install them in Settings → Extensions first');
  for (const { file } of CASES) rmSync(`${project.path}/${file}`, { force: true });
  await browser.close();
  process.exit(1);
}

for (const { file, source } of CASES) writeFileSync(`${project.path}/${file}`, source);

/** The screen point over `token`, found in the editor's own text nodes. */
async function pointOver(token) {
  return page.evaluate((needle) => {
    const content = document.querySelector('.cm-content');
    if (!content) return null;
    const walker = document.createTreeWalker(content, NodeFilter.SHOW_TEXT);
    while (walker.nextNode()) {
      const node = walker.currentNode;
      const at = (node.textContent || '').indexOf(needle);
      if (at < 0) continue;
      const range = document.createRange();
      range.setStart(node, at);
      range.setEnd(node, at + 1);
      const r = range.getBoundingClientRect();
      if (!r.width && !r.height) continue;
      return { x: r.left + r.width / 2, y: r.top + r.height / 2 };
    }
    return null;
  }, token);
}

/**
 * Hover until the card has documentation, or patience runs out. The first hover of a session pays for
 * the server's initialize and its first package load — 10-25s for gopls — so this retries.
 */
async function hoverDocs(token, { allowSeconds = 90 } = {}) {
  const point = await pointOver(token);
  if (!point) return { error: `token "${token}" not found in the editor` };
  const deadline = Date.now() + allowSeconds * 1000;
  while (Date.now() < deadline) {
    // Two moves so the final position is the still one; the card opens after 500ms of stillness.
    await page.mouse.move(point.x - 30, point.y - 30);
    await page.mouse.move(point.x, point.y);
    await page.waitForTimeout(1500);
    const docs = page.locator('.cm-lsp-action-docs');
    if (await docs.first().isVisible().catch(() => false)) {
      const read = await page.evaluate(() => {
        const box = document.querySelector('.cm-lsp-action-docs');
        const block = box?.querySelector('pre') || box?.querySelector('code');
        if (!block) return { block: false, text: (box?.textContent || '').slice(0, 160) };
        const base = getComputedStyle(block).color;
        const spans = [...block.querySelectorAll('span')].map((s) => ({
          text: (s.textContent || '').trim(),
          cls: (s.className || '').trim(),
          color: getComputedStyle(s).color,
        }));
        return {
          block: true,
          base,
          spans,
          html: (block.innerHTML || '').slice(0, 240),
          text: (block.textContent || '').slice(0, 160),
        };
      });
      if (read.block) return read;
      // A card with no code block at all: the server answered in prose, not a signature.
      if (read.text) return { ...read, spans: [], base: null, html: '' };
    }
  }
  return { error: `no hover documentation for "${token}" within ${allowSeconds}s — is its server running for this project?` };
}

/** Colour of the token in the block, or the block's own colour when the token got no span. */
function colorOf(read, text) {
  const span = read.spans?.find((s) => s.text === text);
  return span ? span.color : read.base ?? null;
}

await page.goto('/');
await page.waitForSelector('.project-item', { timeout: 15000 });
await page.locator('.project-item', { hasText: project.name }).first().click();
await page.waitForSelector('.top-bar', { timeout: 10000 });
await page.locator('.explorer-btn').click();

for (const { file, token, keyword } of CASES) {
  await page.locator('.file-browser-item', { hasText: file }).first().click();
  await page.waitForSelector('.cm-editor', { timeout: 20000 });
  await page.waitForTimeout(2000); // the LSP channel opens after the first frame

  const read = await hoverDocs(token);
  if (read.error) {
    check(`${file}: the hover card shows documentation`, false, read.error);
    continue;
  }
  check(`${file}: the hover card shows a code block`, read.block === true, read.text);
  const classed = (read.spans || []).filter((s) => s.cls);
  check(`${file}: the signature block carries classed spans`, classed.length > 0, read.html || read.text);
  const painted = (read.spans || []).filter((s) => s.cls && s.color !== read.base);
  check(`${file}: a class paints — the block is not one flat colour`, painted.length > 0,
    `base ${read.base}, ${painted.length}/${(read.spans || []).length} spans painted`);
  check(`${file}: the "${keyword}" keyword is coloured`,
    painted.some((s) => s.text === keyword),
    `"${keyword}" → ${colorOf(read, keyword)} (block ${read.base})`);
  const keywordColor = colorOf(read, keyword);
  const nameColor = colorOf(read, token);
  check(`${file}: keyword and identifier are different colours`,
    !!keywordColor && !!nameColor && keywordColor !== nameColor,
    `${keyword}=${keywordColor} vs ${token}=${nameColor}`);
  console.log(`      measured ${file}: ${read.text}`);
  console.log(`      spans: ${(read.spans || []).map((s) => `${JSON.stringify(s.text)}=${s.color}${s.cls ? `(${s.cls})` : ''}`).join(' ').slice(0, 300)}`);
  // Dismiss the card so the next file starts from a clean pointer.
  await page.mouse.move(2, 2);
  await page.waitForTimeout(400);
}

for (const { file } of CASES) rmSync(`${project.path}/${file}`, { force: true });
await browser.close();
console.log(failures ? `\n${failures} check(s) failed` : '\nall checks passed');
process.exit(failures ? 1 : 0);
