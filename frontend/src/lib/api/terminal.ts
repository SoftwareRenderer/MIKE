import { API_BASE } from './helpers';

/**
 * Terminal WebSocket connections.
 * Creates WebSocket connections for task terminals and project-level shells.
 */

export function createTerminal(projectId: string, taskId: string, term: import('@xterm/xterm').Terminal, onClosed?: () => void): WebSocket {
    return createTerminalWS(`${window.location.origin}${API_BASE}/projects/${projectId}/tasks/${taskId}/terminal`, term, onClosed);
}

// -- Project Terminal (shell in project root) --
export function createProjectTerminal(projectId: string, term: import('@xterm/xterm').Terminal, onClosed?: () => void): WebSocket {
    return createTerminalWS(`${window.location.origin}${API_BASE}/projects/${projectId}/terminal`, term, onClosed);
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
function createTerminalWS(url: string, term: import('@xterm/xterm').Terminal, onClosed?: () => void): WebSocket {
    // Show status immediately — backend will send its own messages when ready
    term.write('\x1b[2mConnecting...\x1b[0m');
    const ws = new WebSocket(url);
    ws.onopen = () => {
        // Replace connecting message: go to row 1 col 1, clear line
        term.write('\x1b[1H\x1b[2K');
    };
    term.onData((data) => {
        // Filter out xterm.js init queries that the PTY echoes back as garbage text.
        // These are terminal capability queries (DA1, OSC color/title, DCS) sent on mount.
        if (isTerminalQuery(data)) return;
        if (ws.readyState === WebSocket.OPEN) {
            ws.send(JSON.stringify({ type: 'input', data }));
        }
    });
    term.onResize(({ cols, rows }) => {
        if (ws.readyState === WebSocket.OPEN) {
            ws.send(JSON.stringify({ type: 'resize', cols, rows }));
        }
    });
    ws.onmessage = (e) => {
        const msg = JSON.parse(e.data);
        if (msg.type === 'output') term.write(msg.data);
        if (msg.type === 'title') term.title = msg.data;
        if (msg.type === 'status') term.write(msg.data);
        if (msg.type === 'closed' && onClosed) onClosed();
    };
    ws.onclose = () => {
        if (onClosed) onClosed();
    };
    return ws;
}
