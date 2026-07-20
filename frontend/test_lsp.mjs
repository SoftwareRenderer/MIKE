import WebSocket from 'ws';
import https from 'https';

// Login
const loginRes = await new Promise((resolve, reject) => {
  const data = JSON.stringify({ password: 'test' });
  const req = https.request('https://localhost:3000/api/auth/login', {
    method: 'POST',
    rejectUnauthorized: false,
    headers: {
      'Content-Type': 'application/json',
      'Content-Length': Buffer.byteLength(data),
    }
  }, (res) => {
    let body = '';
    res.on('data', (chunk) => body += chunk);
    res.on('end', () => {
      const cookies = res.headers['set-cookie'] || [];
      console.log('Set-Cookie headers:', cookies);
      resolve({ status: res.statusCode, cookies, body });
    });
  });
  req.on('error', reject);
  req.write(data);
  req.end();
});

console.log('Login:', loginRes.status);
const sessionCookie = (loginRes.cookies || []).find(c => c.startsWith('PHPSESSID='));
console.log('Session cookie:', sessionCookie || 'missing');

if (!sessionCookie) {
  console.log('Body:', loginRes.body);
  process.exit(1);
}

// Open LSP WebSocket
const lspWs = new WebSocket('wss://localhost:3000/api/lsp', {
  rejectUnauthorized: false,
  headers: { 'Cookie': sessionCookie.split(';')[0] }
});

lspWs.on('open', () => {
  console.log('WebSocket opened: wss://localhost:3000/api/lsp');
});

lspWs.on('message', (data) => {
  const msg = JSON.parse(data.toString());
  console.log('LSP received:', msg.method ? msg.method : `response id=${msg.id}`, JSON.stringify(msg).substring(0, 150));
});

lspWs.on('error', (err) => {
  console.error('LSP WebSocket error:', err.message);
});

lspWs.on('close', (code, reason) => {
  console.log('WebSocket closed: wss://localhost:3000/api/lsp', code, reason.toString());
});

// Send workspace/subscribe after a delay
setTimeout(() => {
  if (lspWs.readyState === WebSocket.OPEN) {
    const subscribe = {
      method: 'workspace/subscribe',
      params: {
        workspace: '/home/mwren/projects/temp',
        file: '/home/sandbox/temp/hello.go'
      }
    };
    lspWs.send(JSON.stringify(subscribe));
    console.log('WS sent subscribe');

    // Send initialize after subscribe
    setTimeout(() => {
      const init = {
        jsonrpc: '2.0',
        id: 1,
        method: 'initialize',
        params: {
          processId: null,
          clientInfo: { name: '@codemirror/lsp-client' },
          rootUri: 'file:///home/mwren/projects/temp',
          capabilities: {
            general: { markdown: { } },
            textDocument: {
              synchronization: { didSave: true, willSave: true, willSaveWaitUntil: true },
              completion: { completionItem: { snippetSupport: true } },
              hover: { contentFormat: ['markdown'] },
              signatureHelp: { signatureInformation: { documentationFormat: ['markdown'] } }
            }
          }
        }
      };
      lspWs.send(JSON.stringify(init));
      console.log('WS sent initialize');
    }, 500);
  }
}, 2000);

// Wait for responses
setTimeout(() => {
  console.log('WebSocket connections:', [
    lspWs.readyState === WebSocket.OPEN ? 'wss://localhost:3000/api/lsp' : null
  ].filter(Boolean));
  lspWs.close();
  process.exit(0);
}, 15000);
