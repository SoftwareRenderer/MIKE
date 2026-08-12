import type { Transport } from '@codemirror/lsp-client';
import { extensionManager } from '$lib/services/extension-manager.svelte';

/**
 * Create notification handlers that forward LSP messages to a callback
 * instead of the default CodeMirror dialog (cm-lsp-message).
 *
 * Returns a handler map for the LSPClient `notificationHandlers` option.
 * Returning true stops propagation to the default handler.
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

/**
 * Channel ID step for non-overlapping ID ranges per virtual transport.
 * Channel 1: IDs 1001–1999, Channel 2: IDs 2001–2999, etc.
 */
const CHANNEL_ID_STEP = 1000;

/**
 * Derive the language identifier from a file path.
 * Uses installed LSP extensions (with editor features enabled) to determine language support.
 * Falls back to file extension if no extension is found.
 */
export function detectLanguage(filePath: string): string {
  const ext = filePath.split('.').pop()?.toLowerCase() || '';
  // Check if an LSP extension handles this file (editor features enabled)
  const lspExt = extensionManager.getLSPForFile(filePath);
  if (lspExt?.manifest.capabilities?.lsp?.language) {
    return lspExt.manifest.capabilities.lsp.language;
  }
  return ext;
}

/** Check if LSP is available for a given file. */
export function isLSPAvailable(filePath: string): boolean {
  return extensionManager.getLSPForFile(filePath) !== undefined;
}

export interface LSPTransportOptions {
  workspace: string;
  filePath: string;
  onDisconnected?: () => void;
  onServerInfo?: (name: string, version: string | null) => void;
  onReady?: () => void; // Called when backend confirms LSP is fully ready
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
  subscribed: boolean; // whether workspace/subscribe has been sent for this channel
}

/**
 * Manages a single shared WebSocket connection for ALL workspaces,
 * multiplexing multiple editor tabs across multiple projects through
 * virtual transports.
 *
 * Message routing:
 * - Notifications (no `id`) → delivered to channels sharing the same workspace
 *   (determined by matching textDocument.uri against channel workspace paths)
 * - Responses (has `id`) → routed by `_ch` hint or ID range check
 * - $server/lspReady → routed by `_ch` hint
 */
class SharedLSPConnection {
  private ws: WebSocket | null = null;
  private channels = new Map<number, Channel>();
  private nextChannelId = 0;
  private connectPromise: Promise<WebSocket> | null = null;

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

    const wsUrl = `${location.protocol === 'https:' ? 'wss' : 'ws'}://${location.host}/api/lsp`;
    const ws = new WebSocket(wsUrl);
    this.ws = ws;

    this.connectPromise = new Promise<WebSocket>((resolve, reject) => {
      ws.addEventListener('open', () => {
        if (settled) return;
        settle();
        // Install unified dispatcher
        ws.onmessage = (e) => this.dispatchMessage(e.data);
        resolve(ws);
        // Send pending subscriptions for all channels
        this.flushPendingSubscriptions();
      });

      ws.addEventListener('error', () => {
        if (settled) return;
        settle();
        if (this.ws === ws) {
          this.connectPromise = null;
        }
        reject(new Error('LSP WebSocket error'));
      });

      ws.addEventListener('close', () => {
        if (this.ws === ws) {
          this.ws = null;
          // Reset subscription state so pending subs are retried on reconnect
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

  /** Send pending workspace/subscribe messages for all unsubscribed channels. */
  private flushPendingSubscriptions(): void {
    if (!this.ws || this.ws.readyState !== WebSocket.OPEN) return;
    for (const ch of this.channels.values()) {
      if (!ch.subscribed) {
        this.sendSubscribe(ch);
      }
    }
  }

  /** Send workspace/subscribe for a single channel. */
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
      lang: detectLanguage(options.filePath),
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

    // If already connected, subscribe immediately
    if (this.ws?.readyState === WebSocket.OPEN) {
      sendSubscribe();
    } else if (this.connectPromise) {
      // Wait for connection, then subscribe
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
            // Wrap message with channel ID, workspace, language, and file path
            // so the backend can route to the correct language server.
            try {
              const parsed = JSON.parse(msg);
              parsed._ch = channel.id;
              parsed._workspace = channel.workspace;
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

    return transport;
  }

  /** Dispatch an incoming WS message to the appropriate channel(s). */
  private dispatchMessage(data: unknown): void {
    const text = typeof data === 'string' ? data : new TextDecoder().decode(data as ArrayBuffer);

    let parsed: any;
    try {
      parsed = JSON.parse(text);
    } catch {
      // Non-JSON — deliver to all channels
      for (const ch of this.channels.values()) ch.handler?.(text);
      return;
    }

    // $server/lspReady — route to specific channel (or broadcast if no _ch)
    if (parsed.method === '$/lspReady') {
      if (parsed._ch != null) {
        const target = this.channels.get(parsed._ch);
        target?.onReady?.();
      } else {
        for (const ch of this.channels.values()) ch.onReady?.();
      }
      return;
    }

    // $server/lspReset — clear all channels (extension was rebuilt)
    if (parsed.method === '$/lspReset') {
      for (const ch of this.channels.values()) {
        ch.subscribed = false;
        ch.handler = null;
      }
      this.channels.clear();
      return;
    }

    // Notification (no id, has method) → deliver to channels in the same workspace
    if (parsed.id == null && parsed.method) {
      this.dispatchNotification(parsed, text);
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

  /** Route a notification to channels in the same workspace. */
  private dispatchNotification(parsed: any, text: string): void {
    // Try to extract workspace from textDocument.uri
    const uri = parsed?.params?.textDocument?.uri;
    if (uri && typeof uri === 'string') {
      // Strip file:// prefix
      const uriPath = uri.replace(/^file:\/\//, '');
      // Deliver to channels whose workspace is a prefix of the URI path
      for (const ch of this.channels.values()) {
        if (uriPath.startsWith(ch.workspace)) {
          ch.handler?.(text);
        }
      }
      return;
    }

    // No workspace context — broadcast to all channels
    // (e.g., $/progress, window/logMessage)
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

  // Single shared WebSocket connection for all workspaces.
  // Each channel carries its own workspace path via the _workspace hint.
  private connection = new SharedLSPConnection();

  requestChannel(options: LSPTransportOptions): Promise<VirtualTransport> {
    return Promise.resolve(this.connection.requestChannel(options));
  }
}

/**
 * Request a virtual LSP transport from the shared connection manager.
 * All editors across all projects share a single WebSocket connection.
 * The backend routes messages to the correct language server via the
 * _workspace and _lang hints on each message.
 *
 * Returns a standard CodeMirror Transport plus a `release()` method
 * to free the channel when the editor tab is closed.
 */
export function requestLSPChannel(options: LSPTransportOptions): Promise<VirtualTransport> {
  return LSPConnectionManager.getInstance().requestChannel(options);
}
