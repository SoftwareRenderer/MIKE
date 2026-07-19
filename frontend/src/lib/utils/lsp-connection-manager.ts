import type { Transport } from '@codemirror/lsp-client';

/**
 * Channel ID step for non-overlapping ID ranges per virtual transport.
 * Channel 1: IDs 1001–1999, Channel 2: IDs 2001–2999, etc.
 */
const CHANNEL_ID_STEP = 1000;

/**
 * Map file extensions to language identifiers. Must match backend registry.
 * Used to tag messages so the backend routes to the correct language server.
 */
const EXTENSION_TO_LANGUAGE: Record<string, string> = {
  go: 'go',
  ts: 'typescript', tsx: 'typescript', js: 'typescript', jsx: 'typescript', mjs: 'typescript', cjs: 'typescript',
  py: 'python', pyi: 'python',
  rs: 'rust',
};

/** Derive the language identifier from a file path. */
export function detectLanguage(filePath: string): string {
  const ext = filePath.split('.').pop()?.toLowerCase() || '';
  return EXTENSION_TO_LANGUAGE[ext] || ext;
}

export interface LSPTransportOptions {
  workspace: string;
  filePath: string;
  onDisconnected?: () => void;
  onServerInfo?: (name: string, version: string | null) => void;
}

export interface VirtualTransport extends Transport {
  /** Release this channel and its resources. Call when the editor is closed. */
  release: () => void;
}

interface Channel {
  id: number;
  file: string;
  lang: string;
  handler: ((value: string) => void) | null;
  onServerInfo?: (name: string, version: string | null) => void;
}

/**
 * Manages a single WebSocket connection for a workspace, multiplexing
 * multiple editor tabs through virtual transports.
 *
 * Message routing:
 * - Notifications (no `id`) → delivered to ALL channels
 * - Responses (has `id`) → routed by `_ch` hint or ID range check
 */
class WorkspaceConnection {
  private ws: WebSocket | null = null;
  private channels = new Map<number, Channel>();
  private nextChannelId = 0;
  private connectPromise: Promise<WebSocket> | null = null;
  private subscribed = false;

  onDisconnected?: () => void;

  constructor(private workspace: string) {}

  /** Get the first file path from any active channel. */
  private getFirstFile(): string {
    for (const ch of this.channels.values()) {
      if (ch.file) return ch.file;
    }
    return '';
  }

  requestChannel(options: LSPTransportOptions): Promise<VirtualTransport> {
    const channelId = ++this.nextChannelId;
    const channel: Channel = { id: channelId, file: options.filePath, lang: detectLanguage(options.filePath), handler: null, onServerInfo: options.onServerInfo };
    this.channels.set(channelId, channel);

    // If connection is already open but not yet subscribed (no file available),
    // now we have a file — send the subscribe message.
    if (this.ws?.readyState === WebSocket.OPEN && !this.subscribed) {
      this.sendSubscribe();
    }

    const release = () => {
      this.channels.delete(channelId);
      channel.handler = null;
      if (this.channels.size === 0) {
        this.close();
      }
    };

    const transport: VirtualTransport = {
      send: (msg: string) => {
        this.getCurrentWS().then((ws) => {
          if (ws.readyState === WebSocket.OPEN) {
            // Wrap message with channel ID, language, and file path so the backend can
            // route to the correct language server, create it lazily if needed, and
            // route the response back to this channel.
            try {
              const parsed = JSON.parse(msg);
              parsed._ch = channelId;
              parsed._lang = channel.lang;
              parsed._file = channel.file;
              ws.send(JSON.stringify(parsed));
            } catch {
              // Not JSON — send as-is
              ws.send(msg);
            }
          }
        }).catch(() => {});
      },

      subscribe: (fn: (value: string) => void) => {
        channel.handler = fn;
      },

      unsubscribe: () => {
        channel.handler = null;
      },

      release,
    };

    return Promise.resolve(transport);
  }

  private sendSubscribe(): void {
    if (this.subscribed || !this.ws) return;
    const file = this.getFirstFile();
    this.ws.send(JSON.stringify({
      method: 'workspace/subscribe',
      params: { workspace: this.workspace, file },
    }));
    this.subscribed = true;
  }

  /** Dispatch an incoming WS message to the appropriate channel(s). */
  private dispatchMessage(data: unknown): void {
    const text = typeof data === 'string' ? data : new TextDecoder().decode(data as ArrayBuffer);

    let parsed: any;
    try {
      parsed = JSON.parse(text);
    } catch {
      for (const ch of this.channels.values()) ch.handler?.(text);
      return;
    }

    // Notification (no id, has method) → broadcast to ALL channels
    if (parsed.id == null && parsed.method) {
      for (const ch of this.channels.values()) ch.handler?.(text);
      return;
    }

    // Response (has id) → route to owning channel
    if (parsed.id != null) {
      // Backend _ch hint (multiplex mode)
      if (parsed._ch != null) {
        const target = this.channels.get(parsed._ch);
        if (target?.handler) {
          // Extract serverInfo from initialize response
          const si = parsed.result?.serverInfo;
          if (si && target.onServerInfo) {
            target.onServerInfo(si.name || '', si.version || null);
          }
          const clean = { ...parsed };
          delete clean._ch;
          target.handler(JSON.stringify(clean));
        }
        return;
      }

      // Fallback: ID range check (backward compat with non-multiplex backend)
      const idNum = Number(parsed.id);
      if (!isNaN(idNum)) {
        for (const ch of this.channels.values()) {
          const base = ch.id * CHANNEL_ID_STEP;
          if (idNum >= base + 1 && idNum < base + CHANNEL_ID_STEP) {
            // Extract serverInfo from initialize response
            const si = parsed.result?.serverInfo;
            if (si && ch.onServerInfo) {
              ch.onServerInfo(si.name || '', si.version || null);
            }
            ch.handler?.(text);
            return;
          }
        }
      }
      return;
    }

    // Unknown message type — deliver to all
    for (const ch of this.channels.values()) ch.handler?.(text);
  }

  /** Get the current WebSocket, connecting if needed. */
  private getCurrentWS(): Promise<WebSocket> {
    if (this.ws?.readyState === WebSocket.OPEN) {
      return Promise.resolve(this.ws);
    }
    if (this.connectPromise) {
      return this.connectPromise;
    }
    if (this.ws?.readyState === WebSocket.CONNECTING) {
      return new Promise((resolve, reject) => {
        const w = this.ws!;
        const onOpen = () => {
          w.removeEventListener('open', onOpen);
          w.removeEventListener('error', onErr);
          resolve(w);
        };
        const onErr = () => {
          w.removeEventListener('open', onOpen);
          w.removeEventListener('error', onErr);
          reject(new Error('LSP WebSocket failed'));
        };
        w.addEventListener('open', onOpen);
        w.addEventListener('error', onErr);
      });
    }
    return this.doConnect();
  }

  private doConnect(): Promise<WebSocket> {
    let settled = false;
    const settle = () => { settled = true; };

    this.connectPromise = new Promise<WebSocket>((resolve, reject) => {
      const wsUrl = `${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/api/lsp`;
      const ws = new WebSocket(wsUrl);
      this.ws = ws;

      ws.addEventListener('open', () => {
        if (settled) return;
        // Install unified dispatcher before sending subscribe
        ws.onmessage = (e) => this.dispatchMessage(e.data);
        // Send subscribe with first available file
        this.sendSubscribe();
        resolve(ws);
      });

      ws.addEventListener('error', () => {
        if (settled) return;
        settle();
        this.connectPromise = null;
        reject(new Error('LSP WebSocket error'));
      });

      ws.addEventListener('close', () => {
        this.ws = null;
        this.subscribed = false;
        if (this.channels.size === 0) {
          this.onDisconnected?.();
        }
      });
    });

    const p = this.connectPromise!;
    p.catch(() => {});
    p.finally(() => {
      if (!settled) settle();
      this.connectPromise = null;
    });
    return p;
  }

  close(): void {
    if (this.ws) {
      this.ws.onmessage = null;
      this.ws.close();
      this.ws = null;
    }
    this.subscribed = false;
    this.channels.clear();
    this.onDisconnected?.();
  }
}

/** Singleton: one shared manager per page. */
class LSPConnectionManager {
  private static instance: LSPConnectionManager;
  static getInstance(): LSPConnectionManager {
    if (!LSPConnectionManager.instance) {
      LSPConnectionManager.instance = new LSPConnectionManager();
    }
    return LSPConnectionManager.instance;
  }

  // Keyed by workspace path — one WS per workspace, all languages multiplexed.
  // The _lang hint on each message tells the backend which language server to route to.
  private connections = new Map<string, WorkspaceConnection>();

  requestChannel(options: LSPTransportOptions): Promise<VirtualTransport> {
    const { workspace } = options;
    let conn = this.connections.get(workspace);
    if (!conn) {
      conn = new WorkspaceConnection(workspace);
      this.connections.set(workspace, conn);
      conn.onDisconnected = () => this.connections.delete(workspace);
    }
    return conn.requestChannel(options);
  }

  releaseAll(): void {
    for (const [, conn] of this.connections) conn.close();
    this.connections.clear();
  }
}

/**
 * Request a virtual LSP transport from the shared connection manager.
 * All editors in the same workspace share one WebSocket, regardless of language.
 * The backend routes messages to the correct language server via the _lang hint.
 *
 * Returns a standard CodeMirror Transport plus a `release()` method
 * to free the channel when the editor tab is closed.
 */
export function requestLSPChannel(options: LSPTransportOptions): Promise<VirtualTransport> {
  return LSPConnectionManager.getInstance().requestChannel(options);
}

/** Release all shared connections. For testing only. */
export function releaseAllLSPConnections(): void {
  LSPConnectionManager.getInstance().releaseAll();
}
