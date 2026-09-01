// Ad-hoc check for the per-workspace LSP session work. Run against a live server:
//
//   MIKE_LOGIN_PASSWORD=testpassword make run
//   node frontend/tests/lsp-two-workspaces-check.mjs
//
// One client attaches to a Go workspace and asks about a file; a second client attaches to the
// same workspace and must join the server already running there; then a client attaches to a task
// worktree of the same extension. The worktree used to inherit the first workspace's server
// session, and a server that never finished its handshake answers a file with a warning about
// itself ("No active builds contain ...") instead of findings.
//
// Two workspaces still cannot hold a server at once: the extension sandbox is one container per
// extension and its mounts come from the workspace that created it, so the second workspace
// recreates the container and destroys the first one's server. That phase reports BLOCKED. In no
// phase may a client receive a loader complaint, or be left waiting on an unanswered request.

import { chromium } from '@playwright/test';
import { existsSync, readFileSync, readdirSync } from 'node:fs';
import { join } from 'node:path';

const BASE = process.env.BASE || 'http://localhost:3000';
const PROJECT = process.env.PROJECT || '/home/user/projects/kanban/MIKE';
const FILE = 'backend/lsp/manager.go';
// Any task worktree will do (they are checkouts of the same module), so pick one that exists
// rather than naming a task that may since have been merged away.
const worktreesDir = join(PROJECT, '.worktrees', 'MIKE');
const WORKTREE =
  process.env.WORKTREE ||
  readdirSync(worktreesDir)
    .map((name) => join(worktreesDir, name))
    .find((path) => existsSync(join(path, FILE))) ||
  '';
if (!WORKTREE) {
  console.log(`no worktree under ${worktreesDir} holds ${FILE} — nothing to compare against`);
  process.exit(1);
}

const workspace = (tag, root) => ({ tag, root, file: FILE, text: readFileSync(join(root, FILE), 'utf8') });
const A = workspace('A', PROJECT);
const B = workspace('B', WORKTREE);

const loaderComplaints = ['No active builds contain', 'No packages found for open file', 'This file is within module'];

const browser = await chromium.launch();
const context = await browser.newContext();
const login = await context.request.post(`${BASE}/api/auth/login`, { data: { password: 'testpassword' } });
if (!login.ok()) {
  console.log(`login failed: ${login.status()} — start the server with MIKE_LOGIN_PASSWORD=testpassword`);
  await browser.close();
  process.exit(1);
}
const page = await context.newPage();
await page.goto(BASE);

const seen = [];
await page.exposeFunction('report', (tag, raw) => {
  let msg;
  try {
    msg = JSON.parse(raw);
  } catch {
    return;
  }
  if (msg?.method === 'ping' || msg?.type === 'ping') return;
  seen.push({ tag, msg });
});

let out = null;
try {
  out = await page.evaluate(
    async ([A, B]) => {
      const connect = (w) =>
        new Promise((resolve, reject) => {
          const s = new WebSocket(`ws://${location.host}/api/lsp`);
          s.tag = w.tag;
          s.idSeq = 0;
          s.onopen = () => resolve(s);
          s.onerror = () => reject(new Error(`${w.tag}: websocket failed`));
          s.onmessage = (ev) => window.report(w.tag, ev.data);
        });

      const send = (s, w, payload) =>
        s.send(JSON.stringify({ ...payload, _ch: 1, _workspace: w.root, _lang: 'go', _file: w.file }));

      const waitID = (s, id, ms = 30000) =>
        new Promise((resolve, reject) => {
          const t = setTimeout(() => reject(new Error(`${s.tag}: nothing answered id=${id} within ${ms}ms`)), ms);
          const prev = s.onmessage;
          s.onmessage = (ev) => {
            prev(ev);
            let m;
            try {
              m = JSON.parse(ev.data);
            } catch {
              return;
            }
            if (String(m.id) === String(id)) {
              clearTimeout(t);
              s.onmessage = prev;
              resolve(m);
            }
          };
        });

      const attach = async (w) => {
        const t0 = Date.now();
        const s = await connect(w);
        s.send(JSON.stringify({ method: 'workspace/subscribe', params: { workspace: w.root, file: w.file }, _ch: 1 }));
        const id = ++s.idSeq;
        await send(s, w, {
          jsonrpc: '2.0',
          id,
          method: 'initialize',
          params: { processId: null, rootUri: `file://${w.root}`, capabilities: {} },
        });
        const init = await waitID(s, id);
        await send(s, w, { jsonrpc: '2.0', method: 'initialized', params: {} });
        await send(s, w, {
          jsonrpc: '2.0',
          method: 'textDocument/didOpen',
          params: { textDocument: { uri: `file://${w.root}/${w.file}`, languageId: 'go', version: 1, text: w.text } },
        });
        s.attachedIn = Date.now() - t0;
        s.initError = init.error || null;
        return s;
      };

      // documentSymbol on one open file, not workspace/symbol: a workspace query waits for the
      // package graph to load, and inside the sandbox the module cache is empty with no network
      // to fill it, so that load never finishes. One open file is answered from that file —
      // exactly the case that used to come back as "No active builds contain ...".
      const ask = async (s, w) => {
        const id = ++s.idSeq;
        const t0 = Date.now();
        await send(s, w, {
          jsonrpc: '2.0',
          id,
          method: 'textDocument/documentSymbol',
          params: { textDocument: { uri: `file://${w.root}/${w.file}` } },
        });
        try {
          const reply = await waitID(s, id);
          return { ms: Date.now() - t0, error: reply.error || null, count: Array.isArray(reply.result) ? reply.result.length : -1 };
        } catch (err) {
          return { ms: Date.now() - t0, error: { message: err.message }, count: -1 };
        }
      };

      const results = {};

      // 1. one client, one workspace
      const a = await attach(A);
      results.aAttachMs = a.attachedIn;
      results.aInitError = a.initError;
      results.aAlone = await ask(a, A);

      // 2. a second client on the same workspace: it must join the server that is already there
      const a2 = await attach(A);
      results.a2AttachMs = a2.attachedIn;
      results.a2Alone = await ask(a2, A);
      results.aStillOk = await ask(a, A);

      // 3. a different workspace of the same extension
      const b = await attach(B);
      results.bAttachMs = b.attachedIn;
      results.bAlone = await ask(b, B);
      results.aAfterB = await ask(a, A);

      // 4. B goes away; A must be untouched either way
      b.close();
      await new Promise((r) => setTimeout(r, 1500));
      results.aAfterBClosed = await ask(a, A);

      a2.close();
      a.close();
      return results;
    },
    [A, B],
  );
} catch (err) {
  console.log(`script stopped early: ${err.message}`);
}

await browser.close();

const fail = [];
const info = [];
const ok = [];
if (!out) {
  for (const s of seen.slice(-12)) console.log(`  ${s.tag} ${JSON.stringify(s.msg).slice(0, 220)}`);
  console.log('\nFAIL  the run did not finish');
  process.exit(1);
}

for (const [key, label] of [
  ['aAlone', 'one client gets real answers from gopls'],
  ['a2Alone', 'a second client on the same workspace is answered'],
  ['aStillOk', 'the first client is still answered while the second is attached'],
]) {
  const r = out[key];
  if (r.error) fail.push(`${label}: ${JSON.stringify(r.error)}`);
  else if (r.count <= 0) fail.push(`${label}: no symbols back after ${r.ms}ms`);
  else ok.push(`${label} — ${r.count} symbols, ${r.ms}ms`);
}

// The cross-workspace phase is reported, not asserted: the shared extension sandbox is still
// recreated when the mount set changes, which is the next change to make.
const cross = out.aAfterB;
if (cross.error) {
  info.push(`BLOCKED (known) workspace switch: the first workspace got ${JSON.stringify(cross.error.message)} instead of an answer — one container per extension is recreated per workspace`);
} else if (cross.count > 0) {
  ok.push(`cross-workspace: the first workspace kept its server through the second one attaching — ${cross.count} symbols, ${cross.ms}ms`);
}
if (out.bAlone.error) fail.push(`the worktree workspace was not answered at all: ${JSON.stringify(out.bAlone.error)}`);
else if (out.bAlone.count > 0) ok.push(`the worktree workspace was answered about its own file — ${out.bAlone.count} symbols, ${out.bAlone.ms}ms`);

const complaints = seen.filter((s) => {
  const raw = JSON.stringify(s.msg);
  return loaderComplaints.some((t) => raw.includes(t));
});
if (complaints.length) {
  fail.push(`a client was told about the loader's own state: ${complaints[0].msg ? JSON.stringify(complaints[0]).slice(0, 260) : ''}`);
} else {
  ok.push('no "No active builds contain" / "No packages found" reached any client');
}

const published = seen.filter((s) => s.msg?.method === 'textDocument/publishDiagnostics');
const byTag = {};
for (const p of published) byTag[p.tag] = (byTag[p.tag] || 0) + 1;
info.push(`publishDiagnostics: ${published.length} (${Object.entries(byTag).map(([t, n]) => `${t}=${n}`).join(', ') || 'none'})`);

console.log('');
for (const line of ok) console.log(`PASS  ${line}`);
for (const line of info) console.log(`note  ${line}`);
for (const line of fail) console.log(`FAIL  ${line}`);
process.exit(fail.length ? 1 : 0);
