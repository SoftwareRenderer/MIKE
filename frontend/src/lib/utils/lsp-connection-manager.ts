import type { Transport } from '@codemirror/lsp-client';
import { t } from '$lib/i18n';
import { resolveFile } from './editor-language';

/**
 * Notification handlers that forward LSP messages to a callback instead of the default CodeMirror
 * dialog (cm-lsp-message). Returning true from one stops propagation to that default.
 */
export function createLSPNotificationHandlers(
    onMessage: (message: string) => void,
): Record<string, (client: unknown, params: unknown) => boolean> {
    const handle = (_client: unknown, params: unknown) => {
        onMessage((params as { message?: string }).message || '');
        return true;
    };
    return {
        'window/showMessage': handle,
        'window/logMessage': handle,
    };
}

/** The language the backend routes this file to — the manifest's routing key, not the per-file `didOpen` id. */
function routingLanguage(filePath: string): string {
  return resolveFile(filePath).language;
}

/**
 * Whether an LSP channel may open for this file: an enabled extension owns it *and* the user left that
 * extension's editor features on. The gate stops here — the same manifest also declares the file's
 * grammar, and those colors survive the switch (see `ResolvedFile.editorLSP`).
 */
export function isLSPAvailable(filePath: string): boolean {
  const resolved = resolveFile(filePath);
  return resolved.languageId !== '' && resolved.editorLSP;
}

export interface LSPTransportOptions {
  workspace: string;
  filePath: string;
  onDisconnected?: () => void;
  onServerInfo?: (name: string, version: string | null) => void;
  onReady?: () => void;
}

export interface VirtualTransport extends Transport {
  /** Release this channel and its resources. Call when the editor is closed. */
  release: () => void;
}

interface Channel {
  id: number;
  workspace: string;
  file: string;
  lang: string;
  handler: ((value: string) => void) | null;
  onServerInfo?: (name: string, version: string | null) => void;
  onReady?: () => void;
  subscribed: boolean; // workspace/subscribe has been sent for this channel
}

/**
 * Whether the "dropped a response" line has been said yet: the one report of a bookkeeping bug,
 * because a console full of it hides the next one.
 */
let lostResponseReported = false;

/** The one drop on this path that is never silent. */
function reportLostResponse(id: unknown, channel: unknown): void {
  if (lostResponseReported) return;
  lostResponseReported = true;
  console.warn(
    `[lsp] dropped the response to request ${String(id)}: ` +
      (channel == null ? 'no channel was named' : `channel ${String(channel)} has no listener`),
  );
}

/**
 * A single shared WebSocket for every editor tab, multiplexing workspaces and languages over
 * numbered channels. The backend answers each message with its `_ch`, so the answer reaches the
 * channel that asked. Exported so the routing tests can drive dispatchMessage without a socket.
 */
export class SharedLSPConnection {
  private ws: WebSocket | null = null;
  private channels = new Map<number, Channel>();
  private nextChannelId = 0;
  private connectPromise: Promise<WebSocket> | null = null;

  /** The socket to write to, opening one if needed; concurrent callers share one connect. */
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
          reject(new Error(t('lsp.websocketFailed')));
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

    const wsUrl = `${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/api/lsp`;
    const ws = new WebSocket(wsUrl);
    this.ws = ws;

    this.connectPromise = new Promise<WebSocket>((resolve, reject) => {
      ws.addEventListener('open', () => {
        if (settled) return;
        settle();
        ws.onmessage = (e) => this.dispatchMessage(e.data);
        resolve(ws);
        this.flushPendingSubscriptions();
      });

      ws.addEventListener('error', () => {
        if (settled) return;
        settle();
        if (this.ws === ws) {
          this.connectPromise = null;
        }
        reject(new Error(t('lsp.websocketError')));
      });

      ws.addEventListener('close', () => {
        if (this.ws === ws) {
          this.ws = null;
          // The backend forgot every subscription with the socket.
          for (const ch of this.channels.values()) {
            ch.subscribed = false;
          }
        }
      });
    });

    const p = this.connectPromise!;
    p.catch(() => {});
    p.finally(() => {
      if (!settled) settle();
      if (this.ws === ws || this.connectPromise === p) {
        this.connectPromise = null;
      }
    });
    return p;
  }

  /** Re-subscribe every channel: the backend's knowledge of a subscription dies with the socket. */
  private flushPendingSubscriptions(): void {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) return;
    for (const ch of this.channels.values()) {
      if (!ch.subscribed) {
        this.sendSubscribe(ch);
      }
    }
  }

  private sendSubscribe(ch: Channel): void {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) return;
    this.ws.send(JSON.stringify({
      _ch: ch.id,
      method: 'workspace/subscribe',
      params: { workspace: ch.workspace, file: ch.file },
    }));
    ch.subscribed = true;
  }

  /** Create a new channel and return its virtual transport. */
  requestChannel(options: LSPTransportOptions): VirtualTransport {
    const channelId = ++this.nextChannelId;
    const channel: Channel = {
      id: channelId,
      workspace: options.workspace,
      file: options.filePath,
      lang: routingLanguage(options.filePath),
      handler: null,
      onServerInfo: options.onServerInfo,
      onReady: options.onReady,
      subscribed: false,
    };
    this.channels.set(channelId, channel);

    const sendSubscribe = () => {
      this.getCurrentWS().then((ws) => {
        if (ws.readyState === WebSocket.OPEN && !channel.subscribed) {
          this.sendSubscribe(channel);
        }
      }).catch(() => {});
    };

    if (this.ws?.readyState === WebSocket.OPEN) {
      sendSubscribe();
    } else if (this.connectPromise) {
      this.connectPromise.then(() => sendSubscribe()).catch(() => {});
    }

    const release = () => {
      this.channels.delete(channelId);
      channel.handler = null;
    };

    const transport: VirtualTransport = {
      send: (msg: string) => {
        this.getCurrentWS().then((ws) => {
          if (ws.readyState === WebSocket.OPEN) {
            // The hints are how one socket reaches one language server.
            try {
              const parsed = JSON.parse(msg);
              parsed._ch = channel.id;
              parsed._workspace = channel.workspace;
              parsed._lang = channel.lang;
              parsed._file = channel.file;
              ws.send(JSON.stringify(parsed));
            } catch {
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

    return transport;
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

    if (parsed.method === '$/lspReady') {
      if (parsed._ch != null) {
        const target = this.channels.get(parsed._ch);
        target?.onReady?.();
      } else {
        for (const ch of this.channels.values()) ch.onReady?.();
      }
      return;
    }

    // The extension was rebuilt: every channel must re-subscribe.
    if (parsed.method === '$/lspReset') {
      for (const ch of this.channels.values()) {
        ch.subscribed = false;
        ch.handler = null;
      }
      this.channels.clear();
      return;
    }

    if (parsed.id == null && parsed.method) {
      this.dispatchNotification(parsed, text);
      return;
    }

    // A response carries the `_ch` of the channel that asked.
    if (parsed.id != null) {
      const target = parsed._ch != null ? this.channels.get(parsed._ch) : undefined;
      if (target?.handler) {
        const si = parsed.result?.serverInfo;
        if (si && target.onServerInfo) {
          target.onServerInfo(si.name || '', si.version || null);
        }
        const clean = { ...parsed };
        delete clean._ch;
        target.handler(JSON.stringify(clean));
        return;
      }
      // Nothing is listening for it. Thrown away quietly, this looks to the editor exactly like a
      // language server that never answered, which is the failure this whole path exists to avoid.
      reportLostResponse(parsed.id, parsed._ch);
      return;
    }

    for (const ch of this.channels.values()) ch.handler?.(text);
  }

  /** Route a notification to channels in the same workspace. */
  private dispatchNotification(parsed: any, text: string): void {
    const uri = parsed?.params?.textDocument?.uri;
    if (uri && typeof uri === 'string') {
      // A notification names a document, not a channel, so the workspace path is the only match.
      const uriPath = uri.replace(/^file:\/\//, '');
      for (const ch of this.channels.values()) {
        if (uriPath.startsWith(ch.workspace)) {
          ch.handler?.(text);
        }
      }
      return;
    }

    // Nothing names a workspace ($/progress, window/logMessage), so every channel may care.
    for (const ch of this.channels.values()) ch.handler?.(text);
  }

  close(): void {
    if (this.ws) {
      this.ws.onmessage = null;
      this.ws.close();
      this.ws = null;
    }
    this.channels.clear();
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

  private connection = new SharedLSPConnection();

  requestChannel(options: LSPTransportOptions): Promise<VirtualTransport> {
    return Promise.resolve(this.connection.requestChannel(options));
  }
}

/**
 * Request a virtual LSP transport from the shared connection manager. All editors share one
 * socket, and a language server is scoped to a workspace and a language, so each tab's messages
 * must be distinguishable — the LSPClient's own `virtual` option is not the answer because the
 * shared socket outlives any one extension.
 */
export function requestLSPChannel(options: LSPTransportOptions): Promise<VirtualTransport> {
  return LSPConnectionManager.getInstance().requestChannel(options);
}
