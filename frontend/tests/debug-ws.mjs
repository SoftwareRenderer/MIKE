import https from 'https';
import WebSocket from 'ws';

const agent = new https.Agent({ rejectUnauthorized: false });

async function login() {
  return new Promise((resolve, reject) => {
    const req = https.request('https://localhost:3000/api/auth/login', {
      method: 'POST', headers: { 'Content-Type': 'application/json' }, agent
    }, (res) => {
      res.on('data', () => {});
      res.on('end', () => resolve(res.headers['set-cookie']));
    });
    req.on('error', reject);
    req.write(JSON.stringify({ password: 'test' }));
    req.end();
  });
}

function getCsrf(cookieStr) {
  return new Promise((resolve, reject) => {
    const req = https.request('https://localhost:3000/api/auth/session', {
      headers: { Cookie: cookieStr }, agent
    }, (res) => {
      let data = '';
      res.on('data', d => data += d);
      res.on('end', () => resolve(JSON.parse(data).csrf_token || ''));
    });
    req.on('error', reject);
    req.end();
  });
}

// Reset task
async function resetTask(cookieStr, csrf) {
  return new Promise((resolve) => {
    const req = https.request('https://localhost:3000/api/projects/temp/tasks/chant-granite-cobra', {
      method: 'PUT', headers: { 'Content-Type': 'application/json', Cookie: cookieStr, 'X-CSRF-Token': csrf }, agent
    }, (res) => {
      res.on('data', () => {});
      res.on('end', () => resolve());
    });
    req.on('error', () => resolve());
    req.write(JSON.stringify({ execution_state: 'idle', status: 'todo' }));
    req.end();
  });
}

async function main() {
  const cookies = await login();
  const sessionCookie = cookies.find(c => c.startsWith('PHPSESSID='));
  const cookieStr = sessionCookie.split(';')[0];
  const csrf = await getCsrf(cookieStr);

  await resetTask(cookieStr, csrf);
  console.log('Task reset, starting agent...');

  // Start the agent via HTTP
  const startRes = await new Promise((resolve) => {
    const req = https.request('https://localhost:3000/api/projects/temp/tasks/chant-granite-cobra/send', {
      method: 'POST', headers: { 'Content-Type': 'application/json', Cookie: cookieStr, 'X-CSRF-Token': csrf }, agent
    }, (res) => {
      let data = '';
      res.on('data', d => data += d);
      res.on('end', () => resolve(data));
    });
    req.on('error', () => resolve(''));
    req.write(JSON.stringify({ content: 'hey' }));
    req.end();
  });
  console.log('Agent started:', startRes);

  // Wait a moment for agent to start
  await new Promise(r => setTimeout(r, 1000));

  // Connect WebSocket
  const ws = new WebSocket('wss://localhost:3000/api/stream', {
    headers: { Cookie: cookieStr },
    rejectUnauthorized: false
  });

  let eventCount = 0;
  const keyEvents = [];

  ws.on('open', () => {
    console.log('WebSocket connected');
    ws.send(JSON.stringify({ type: 'subscribe_task', data: { project_id: 'temp', task_id: 'chant-granite-cobra' } }));
  });

  ws.on('message', (data) => {
    try {
      const msg = JSON.parse(data.toString());
      if (msg.type && msg.type !== 'ping') {
        eventCount++;
        if (msg.type === 'task_event') {
          const d = msg.data;
          if (d.model_name) {
            keyEvents.push(`stats: model=${d.model_name} prompt=${d.prompt_tokens} predicted=${d.predicted_tokens}`);
          } else if (d.text) {
            keyEvents.push(`token: "${d.text}"`);
          } else if (d.state) {
            keyEvents.push(`status: ${d.state} ${d.activity || ''}`);
          }
        } else if (msg.type === 'complete') {
          keyEvents.push(`*** COMPLETE: state=${msg.data.state} model=${msg.data.model_name} ***`);
        } else if (msg.type === 'task_updated') {
          keyEvents.push(`task_updated: status=${msg.data.status} state=${msg.data.execution_state}`);
        }
      }
    } catch(e) {}
  });

  ws.on('error', (err) => {
    console.log('WebSocket error:', err.message);
  });

  // Wait 25 seconds
  await new Promise(resolve => setTimeout(resolve, 25000));

  ws.close();

  console.log(`\n=== Total events: ${eventCount} ===`);
  console.log('=== Key Events ===');
  keyEvents.slice(-40).forEach(e => console.log(e));
}

main().catch(console.error);
