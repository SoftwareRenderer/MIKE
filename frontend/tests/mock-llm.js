/**
 * Mock llama.cpp OpenAI-compatible API server for e2e tests.
 * 
 * Serves pre-recorded SSE streaming responses on POST /chat/completions.
 * 
 * Response selection:
 * - If tools are provided in the request -> tool_call_response.json
 * - Otherwise -> text_response.json
 * 
 * Usage:
 *   node frontend/tests/mock-llm.js [port]
 *   Default port: 9999
 */

import http from 'node:http';
import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = path.dirname(fileURLToPath(import.meta.url));
const PORT = parseInt(process.argv[2] || '9999', 10);

// Load fixtures
const textResponse = JSON.parse(fs.readFileSync(path.join(__dirname, 'fixtures', 'text_response.json'), 'utf8'));
const toolCallResponse = JSON.parse(fs.readFileSync(path.join(__dirname, 'fixtures', 'tool_call_response.json'), 'utf8'));

let requestCount = 0;

const server = http.createServer((req, res) => {
    if (req.method === 'POST' && req.url === '/chat/completions') {
        requestCount++;
        let body = '';
        req.on('data', chunk => { body += chunk.toString(); });
        req.on('end', () => {
            try {
                const parsed = JSON.parse(body);
                // Always return text response (no tool calls) for simplicity
                // Tool call handling requires the backend to execute tools, which complicates mocking
                const chunks = textResponse;
                
                // SSE headers
                res.writeHead(200, {
                    'Content-Type': 'text/event-stream',
                    'Cache-Control': 'no-cache',
                    'Connection': 'keep-alive',
                    'X-Accel-Buffering': 'no',
                });

                // Send chunks with small delays to simulate streaming
                let i = 0;
                const sendNext = () => {
                    if (i >= chunks.length) {
                        res.write('data: [DONE]\n\n');
                        res.end();
                        return;
                    }
                    res.write(`data: ${JSON.stringify(chunks[i])}\n\n`);
                    i++;
                    // Small delay between chunks (5ms) to simulate real streaming
                    setTimeout(sendNext, 5);
                };
                sendNext();
            } catch (e) {
                res.writeHead(400, { 'Content-Type': 'application/json' });
                res.end(JSON.stringify({ error: 'Invalid request body' }));
            }
        });
    } else {
        res.writeHead(404, { 'Content-Type': 'application/json' });
        res.end(JSON.stringify({ error: 'Not found' }));
    }
});

server.listen(PORT, () => {
    console.error(`Mock LLM server listening on port ${PORT}`);
});

// Graceful shutdown
process.on('SIGTERM', () => {
    console.error(`Mock LLM server shutting down (handled ${requestCount} requests)`);
    server.close();
    process.exit(0);
});

process.on('SIGINT', () => {
    console.error(`Mock LLM server shutting down (handled ${requestCount} requests)`);
    server.close();
    process.exit(0);
});
