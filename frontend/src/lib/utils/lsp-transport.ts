import type { Transport } from '@codemirror/lsp-client';

export interface LSPTransportOptions {
  workspace: string;   // absolute workspace path
  filePath: string;    // absolute file path
  onConnected?: () => void;
  onDisconnected?: () => void;
}

/**
 * Build a WebSocket-based Transport for @codemirror/lsp-client.
 *
 * Connects to the Go server's LSP proxy endpoint and wires send/subscribe
 * so the LSP client library can communicate with a language server.
 */
export function createLSPTransport(options: LSPTransportOptions): Promise<Transport> {
  return new Promise((resolve, reject) => {
    const wsUrl = `${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/api/lsp`;
    const ws = new WebSocket(wsUrl);

    let currentHandler: ((value: string) => void) | null = null;

    ws.onopen = () => {
      // Subscribe to workspace before resolving
      ws.send(JSON.stringify({
        method: 'workspace/subscribe',
        params: { workspace: options.workspace, file: options.filePath },
      }));
      options.onConnected?.();
      resolve({
        send(msg: string) {
          if (ws.readyState === WebSocket.OPEN) ws.send(msg);
        },
        subscribe(fn: (value: string) => void) {
          currentHandler = fn;
          ws.onmessage = (e: MessageEvent) =>
            fn(typeof e.data === 'string' ? e.data : new TextDecoder().decode(e.data));
        },
        unsubscribe(fn: (value: string) => void) {
          if (currentHandler === fn) {
            currentHandler = null;
            ws.onmessage = null;
          }
        },
      });
    };

    ws.onerror = () => reject(new Error('LSP WebSocket error'));
    ws.onclose = () => {
      options.onDisconnected?.();
      // Don't reject if already resolved — client handles disconnect via `connected` property
    };
  });
}
