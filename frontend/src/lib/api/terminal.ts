import { API_BASE, parseStreamFrame } from './helpers';
import { STALE_THRESHOLD_MS } from '$lib/constants';
import { t } from '$lib/i18n';

// Track last activity per terminal WebSocket for stale connection detection
const terminalActivity = new WeakMap<WebSocket, number>();

// The newest socket per URL. Only that one speaks for the shell: a retired socket's `close`
// arrives after its replacement exists, and reading it as the shell gone would kill that one.
const currentSocket = new Map<string, WebSocket>();

/** Retire `ws` as the socket speaking for `url`; false if a newer one already took its place. */
function retireSocket(url: string, ws: WebSocket): boolean {
    if (currentSocket.get(url) !== ws) return false;
    currentSocket.delete(url);
    return true;
}

/**
 * Terminal WebSocket connections.
 * Creates WebSocket connections for task terminals and project-level shells.
 */

export function createTerminal(projectId: string, taskId: string, term: import('@xterm/xterm').Terminal, onClosed?: () => void, transformInput?: (data: string) => string): WebSocket {
    return createTerminalWS(`${window.location.origin}${API_BASE}/projects/${projectId}/tasks/${taskId}/terminal`, term, onClosed, transformInput);
}

// -- Project Terminal (shell in project root) --
export function createProjectTerminal(projectId: string, term: import('@xterm/xterm').Terminal, onClosed?: () => void, transformInput?: (data: string) => string): WebSocket {
    return createTerminalWS(`${window.location.origin}${API_BASE}/projects/${projectId}/terminal`, term, onClosed, transformInput);
}

// Filter xterm.js init queries that the PTY echoes back as visible garbage.
// These are terminal capability probes sent automatically on terminal creation.
function isTerminalQuery(data: string): boolean {
    if (!data.startsWith('\x1b')) return false;
    const second = data[1];
    // DCS sequence (e.g. charset detection)
    if (second === 'P') return true;
    // OSC queries: title (]0;?), background color (]10;?), foreground (]11;?), cursor (]12;?)
    if (second === ']') {
        return /^\x1b\](0|10|11|12);\?\x07?$/.test(data);
    }
    // DSR queries: device attributes (c), secondary device attributes ($y)
    if (second === '[') {
        return data === '\x1b[c' || data.startsWith('\x1b[$y');
    }
    return false;
}

// Shared WebSocket setup for terminal connections
// Returns the WebSocket and sets ws.onClose handler to call onClosed callback.
function createTerminalWS(url: string, term: import('@xterm/xterm').Terminal, onClosed?: () => void, transformInput?: (data: string) => string): WebSocket {
    // Show status immediately — backend will send its own messages when ready
    term.write(`\x1b[2m${t('terminal.connecting')}\x1b[0m`);
    const ws = new WebSocket(url);
    terminalActivity.set(ws, Date.now());
    currentSocket.set(url, ws);
    ws.onopen = () => {
        terminalActivity.set(ws, Date.now());
        // Replace connecting message: go to row 1 col 1, clear line
        term.write('\x1b[1H\x1b[2K');
        // Sync viewport size — mount-time fit() fires before this handler
        // registers, so the backend keeps its default size until resized.
        ws.send(JSON.stringify({ type: 'resize', cols: term.cols, rows: term.rows }));
    };
    term.onData((data) => {
        // Filter out xterm.js init queries that the PTY echoes back as garbage text.
        // These are terminal capability queries (DA1, OSC color/title, DCS) sent on mount.
        if (isTerminalQuery(data)) return;
        const transformed = transformInput ? transformInput(data) : data;
        if (ws.readyState === WebSocket.OPEN) {
            ws.send(JSON.stringify({ type: 'input', data: transformed }));
        }
    });
    term.onResize(({ cols, rows }) => {
        if (ws.readyState === WebSocket.OPEN) {
            ws.send(JSON.stringify({ type: 'resize', cols, rows }));
        }
    });
    ws.onmessage = (e) => {
        terminalActivity.set(ws, Date.now());
        const msg = parseStreamFrame(e.data, 'terminal');
        if (!msg) return; // an unreadable frame is dropped; the shell stays connected
        if (msg.type === 'output') term.write(msg.data);
        if (msg.type === 'title') (term as any).title = msg.data; // xterm Terminal lacks .title in types
        if (msg.type === 'status') term.write(msg.data);
        if (msg.type === 'closed' && retireSocket(url, ws) && onClosed) onClosed();
    };
    ws.onclose = () => {
        if (retireSocket(url, ws) && onClosed) onClosed();
    };
    return ws;
}

/** Check if a terminal WebSocket is stale (zombie) and should be closed.
 * Returns true if the connection appears open but hasn't received data in too long. */
export function isTerminalStale(ws: WebSocket): boolean {
    if (ws.readyState !== WebSocket.OPEN) return false;
    const lastActivity = terminalActivity.get(ws);
    if (!lastActivity) return false;
    return Date.now() - lastActivity > STALE_THRESHOLD_MS;
}

