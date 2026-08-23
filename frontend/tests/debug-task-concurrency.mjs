// Diagnostic: reproduce provider-task-concurrency e2e scenario against a
// manually-started logged server (port 3001) to inspect [title-gen] log lines.
// Run: node frontend/tests/debug-task-concurrency.mjs
import http from 'node:http';

const BASE = 'http://localhost:3001';
const ts = Date.now();

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
        const setCookie = res.headers['set-cookie'];
        let json = null;
        try { json = buf ? JSON.parse(buf) : null; } catch {}
        resolve({ status: res.statusCode, json, setCookie });
      });
    });
    req.on('error', reject);
    if (data) req.write(data);
    req.end();
  });
}

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

async function main() {
  // login
  const loginRes = await request('POST', '/api/auth/login', { password: 'test-password-1234' });
  const cookie = (loginRes.setCookie || []).map((c) => c.split(';')[0]).find((c) => c.startsWith('PHPSESSID='));
  const session = await request('GET', '/api/auth/session', null, cookie);
  const csrf = session.json.csrf_token;
  console.log('logged in');

  // config: mock provider, worker+utility, concurrency 1
  let cfg = (await request('GET', '/api/config', null, cookie)).json;
  await request('PUT', '/api/config', {
    ...cfg,
    providers: [{
      name: 'mock', display_name: 'Mock LLM', url: 'http://localhost:9999/v1',
      models: [{ name: 'mock-model', reasoning_effort: 'off' }],
      concurrency: 1,
    }],
    worker_provider: 'mock', worker_model: 'mock-model',
    utility_provider: 'mock', utility_model: 'mock-model',
  }, cookie, csrf);
  console.log('provider configured (concurrency=1)');

  // project
  const proj = (await request('POST', '/api/projects', { name: 'test-project', path: '/tmp/kanban-test-repo' }, cookie, csrf)).json;
  console.log('project:', proj.id);

  // task 1
  const mkPrompt = (n) => `concurrency slow-start stop-test ${n} ${ts}`;
  const taskBody = (prompt) => ({ prompt, llama_server_url: 'http://localhost:9999/v1', model_name: 'mock-model' });
  const t1 = (await request('POST', `/api/projects/${proj.id}/tasks`, taskBody(mkPrompt('one alpha')), cookie, csrf)).json;
  console.log('T1 created', t1.id, 'title=', JSON.stringify(t1.title));
  await request('POST', `/api/projects/${proj.id}/tasks/${t1.id}/start`, {}, cookie, csrf);
  const t0 = Date.now();
  const log = (what) => console.log(`t+${((Date.now() - t0) / 1000).toFixed(1)}s ${what}`);

  const getState = async (tid) => (await request('GET', `/api/projects/${proj.id}/tasks/${tid}`, null, cookie)).json;

  // wait for T1 running
  for (let i = 0; i < 60; i++) {
    const s = await getState(t1.id);
    if (s.status === 'in_progress' && s.execution_state === 'running') break;
    await sleep(250);
  }
  log('T1 running');

  // tasks 2 and 3 (created while T1 stream in flight, then started)
  const t2 = (await request('POST', `/api/projects/${proj.id}/tasks`, taskBody(mkPrompt('two beta')), cookie, csrf)).json;
  log('T2 created title=', JSON.stringify(t2.title));
  const t3 = (await request('POST', `/api/projects/${proj.id}/tasks`, taskBody(mkPrompt('three gamma')), cookie, csrf)).json;
  log('T3 created title=', JSON.stringify(t3.title));
  await request('POST', `/api/projects/${proj.id}/tasks/${t2.id}/start`, {}, cookie, csrf);
  await request('POST', `/api/projects/${proj.id}/tasks/${t3.id}/start`, {}, cookie, csrf);
  log('T2,T3 started');

  // poll timeline
  const ids = [t1.id, t2.id, t3.id];
  const names = ['T1', 'T2', 'T3'];
  let lastSig = '';
  const deadline = Date.now() + 120000;
  while (Date.now() < deadline) {
    const rows = [];
    for (let i = 0; i < ids.length; i++) {
      const s = await getState(ids[i]);
      rows.push(`${names[i]}:${s.status}/${s.execution_state} title=${JSON.stringify(s.title)}`);
    }
    const sig = rows.join('|');
    if (sig !== lastSig) { log(sig); lastSig = sig; }
    const allReview = rows.every((r) => r.startsWith('T') && (/:review\//.test(r)));
    if (allReview) break;
    await sleep(300);
  }
  console.log('done');
}

main().catch((e) => { console.error(e); process.exit(1); });
