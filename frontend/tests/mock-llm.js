/**
 * Mock llama.cpp OpenAI-compatible API server for e2e tests.
 * 
 * Serves pre-recorded SSE streaming responses on POST /chat/completions.
 * 
 * Response routing:
 * - Tools in request + "list"/"file"/"directory" in messages → tool_call_response.json (fast)
 * - "stop" in messages → text_response_slow.json (100ms between chunks for stop button test)
 * - "long"/"detailed"/"write" in messages → text_response_long.json (fast)
 * - Default → text_response.json (fast)
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
const FIXTURES_DIR = path.join(__dirname, 'fixtures');
const PORT = parseInt(process.argv[2] || '9999', 10);

// Load fixtures
function loadFixture(name) {
    return JSON.parse(fs.readFileSync(path.join(FIXTURES_DIR, `${name}.json`), 'utf8'));
}

const textResponse = loadFixture('text_response');
const textResponseLong = loadFixture('text_response_long');
const textResponseSlow = loadFixture('text_response_slow');
const toolCallResponse = loadFixture('tool_call_response');

let requestCount = 0;

/**
 * Select fixture based on request body content.
 * Returns { chunks, chunkDelay } where chunkDelay is ms between SSE frames.
 */
function selectResponse(parsed) {
    const hasTools = parsed.tools && parsed.tools.length > 0;
    const messages = parsed.messages || [];
    const userMessages = messages
        .filter(m => m.role === 'user')
        .map(m => (m.content || '').toLowerCase())
        .join(' ');

    // Tool call scenario: tools provided and message mentions file/directory operations
    if (hasTools && /list|file|directory|read|command|git|exec/i.test(userMessages)) {
        return { chunks: toolCallResponse, chunkDelay: 5 };
    }

    // Slow response: for stop button test (needs streaming to be visible)
    if (/stop button|stop-test/i.test(userMessages)) {
        return { chunks: textResponseSlow, chunkDelay: 100 };
    }

    // Long response scenario: message asks for detailed/long output
    if (/long|detailed|write a|explain|describe/i.test(userMessages)) {
        return { chunks: textResponseLong, chunkDelay: 5 };
    }

    // Default: simple text response
    return { chunks: textResponse, chunkDelay: 5 };
}

const server = http.createServer((req, res) => {
    if (req.method === 'POST' && req.url === '/chat/completions') {
        requestCount++;
        let body = '';
        req.on('data', chunk => { body += chunk.toString(); });
        req.on('end', () => {
            try {
                const parsed = JSON.parse(body);
                const { chunks, chunkDelay } = selectResponse(parsed);
                const fixtureName = chunks === toolCallResponse ? 'tool_call' :
                                    chunks === textResponseSlow ? 'text_slow' :
                                    chunks === textResponseLong ? 'text_long' : 'text';
                console.error(`[mock-llm] request #${requestCount}: routing to ${fixtureName} (delay=${chunkDelay}ms)`);

                // SSE headers
                res.writeHead(200, {
                    'Content-Type': 'text/event-stream',
                    'Cache-Control': 'no-cache',
                    'Connection': 'keep-alive',
                    'X-Accel-Buffering': 'no',
                });

                // Small initial delay to simulate LLM warm-up (200ms)
                setTimeout(() => {
                    let i = 0;
                    const sendNext = () => {
                        if (i >= chunks.length) {
                            res.write('data: [DONE]\n\n');
                            res.end();
                            return;
                        }
                        res.write(`data: ${JSON.stringify(chunks[i])}\n\n`);
                        i++;
                        setTimeout(sendNext, chunkDelay);
                    };
                    sendNext();
                }, 200);
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
