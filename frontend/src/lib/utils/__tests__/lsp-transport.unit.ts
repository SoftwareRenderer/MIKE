import { describe, it, expect, beforeEach } from 'vitest';
import { detectLanguage } from '$lib/utils/lsp-connection-manager';
import { extensionManager } from '$lib/services/extension-manager.svelte';
import type { Extension } from '$lib/types';

/**
 * Tests for the LSP connection manager's message routing logic.
 *
 * The routing logic is extracted here for direct unit testing without
 * WebSocket mocking (jsdom WebSocket mocking is unreliable).
 * Connection lifecycle tests are covered by the backend handler tests.
 */

const CHANNEL_ID_STEP = 1000;

/** Helper to add a mock LSP extension to the manager. */
function addMockLSPExt(name: string, language: string, extensions: string[]) {
    const ext: Extension = {
        id: `${name}-1`,
        name,
        version: '1.0.0',
        type: 'lsp',
        manifest: {
            display_name: name,
            description: '',
            author: 'Test',
            deprecated: false,
            lsp: {
                command: name,
                args: [],
                extensions,
                language,
            },
        },
        config: {},
        container_config: {},
        sandbox_config: { network_isolation: true, memory_limit: 256 },
        install_source: 'manual',
        enabled: true,
        running: false,
        installed_at: new Date().toISOString(),
        updated_at: new Date().toISOString(),
    };
    extensionManager.state.extensions.push(ext);
}

describe('detectLanguage', () => {
  beforeEach(() => {
    // Reset extensions before each test
    extensionManager.state.extensions = [];
  });

  it('falls back to lowercase extension when no LSP extension installed', () => {
    expect(detectLanguage('main.go')).toBe('go');
    expect(detectLanguage('app.ts')).toBe('ts');
    expect(detectLanguage('config.toml')).toBe('toml');
  });

  it('maps Go to go when gopls extension is installed', () => {
    addMockLSPExt('gopls', 'go', ['go']);
    expect(detectLanguage('main.go')).toBe('go');
  });

  it('maps TypeScript extensions to typescript when tsserver extension is installed', () => {
    addMockLSPExt('tsserver', 'typescript', ['ts', 'tsx', 'js', 'jsx', 'mjs', 'cjs']);
    expect(detectLanguage('app.ts')).toBe('typescript');
    expect(detectLanguage('app.tsx')).toBe('typescript');
    expect(detectLanguage('app.js')).toBe('typescript');
    expect(detectLanguage('app.jsx')).toBe('typescript');
    expect(detectLanguage('app.mjs')).toBe('typescript');
    expect(detectLanguage('app.cjs')).toBe('typescript');
  });

  it('maps Python extensions to python when pylsp extension is installed', () => {
    addMockLSPExt('pylsp', 'python', ['py', 'pyi']);
    expect(detectLanguage('main.py')).toBe('python');
    expect(detectLanguage('stub.pyi')).toBe('python');
  });

  it('maps Rust extensions to rust when rust-analyzer extension is installed', () => {
    addMockLSPExt('rust-analyzer', 'rust', ['rs']);
    expect(detectLanguage('main.rs')).toBe('rust');
  });

  it('falls back to lowercase extension for unknown types even with extensions installed', () => {
    addMockLSPExt('gopls', 'go', ['go']);
    expect(detectLanguage('config.toml')).toBe('toml');
    expect(detectLanguage('README.md')).toBe('md');
  });
});

interface Channel {
  id: number;
  workspace: string;
  handler: ((value: string) => void) | null;
  onServerInfo?: (name: string, version: string | null) => void;
  onReady?: () => void;
}

/**
 * Simulates the dispatchMessage logic from SharedLSPConnection.
 * This is the core routing logic that determines which channel gets which message.
 * Notifications are scoped by workspace (via textDocument.uri matching).
 */
function dispatchMessage(channels: Channel[], data: string): void {
  let parsed: any;
  try {
    parsed = JSON.parse(data);
  } catch {
    // Not JSON — deliver to all
    for (const ch of channels) ch.handler?.(data);
    return;
  }

  // $server/lspReady — route to specific channel (or broadcast if no _ch)
  if (parsed.method === '$/lspReady') {
    if (parsed._ch != null) {
      const target = channels.find(ch => ch.id === parsed._ch);
      target?.onReady?.();
    } else {
      for (const ch of channels) ch.onReady?.();
    }
    return;
  }

  // Notification (no id, has method) → scope by workspace
  if (parsed.id == null && parsed.method) {
    const uri = parsed?.params?.textDocument?.uri;
    if (uri && typeof uri === 'string') {
      const uriPath = uri.replace(/^file:\/\//, '');
      for (const ch of channels) {
        if (uriPath.startsWith(ch.workspace)) {
          ch.handler?.(data);
        }
      }
      return;
    }
    // No workspace context — broadcast to all (e.g., $/progress, window/logMessage)
    for (const ch of channels) ch.handler?.(data);
    return;
  }

  // Response (has id) → route to owning channel
  if (parsed.id != null) {
    // Backend _ch hint (multiplex mode)
    if (parsed._ch != null) {
      const target = channels.find(ch => ch.id === parsed._ch);
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

    // Fallback: ID range check (backward compat)
    const idNum = Number(parsed.id);
    if (!isNaN(idNum)) {
      for (const ch of channels) {
        const base = ch.id * CHANNEL_ID_STEP;
        if (idNum >= base + 1 && idNum < base + CHANNEL_ID_STEP) {
          // Extract serverInfo from initialize response
          const si = parsed.result?.serverInfo;
          if (si && ch.onServerInfo) {
            ch.onServerInfo(si.name || '', si.version || null);
          }
          ch.handler?.(data);
          return;
        }
      }
    }
    return;
  }

  // Unknown message type — deliver to all
  for (const ch of channels) ch.handler?.(data);
}

describe('LSP connection manager — message routing', () => {
  let channels: Channel[];
  let received: string[][];

  function setup(channelCount: number, workspace = '/tmp/project') {
    channels = [];
    received = [];
    for (let i = 1; i <= channelCount; i++) {
      channels.push({ id: i, workspace, handler: null });
      received.push([]);
    }
    // Wire up handlers
    for (let i = 0; i < channels.length; i++) {
      const idx = i;
      channels[i].handler = (msg) => received[idx].push(msg);
    }
  }

  function setupMultiWorkspace(workspaces: string[]) {
    channels = [];
    received = [];
    for (let i = 0; i < workspaces.length; i++) {
      channels.push({ id: i + 1, workspace: workspaces[i], handler: null });
      received.push([]);
    }
    // Wire up handlers
    for (let i = 0; i < channels.length; i++) {
      const idx = i;
      channels[i].handler = (msg) => received[idx].push(msg);
    }
  }

  describe('notifications — single workspace', () => {
    beforeEach(() => setup(3));

    it('delivers diagnostics to all channels in the same workspace', () => {
      dispatchMessage(channels, JSON.stringify({
        jsonrpc: '2.0',
        method: 'textDocument/publishDiagnostics',
        params: { textDocument: { uri: 'file:///tmp/project/main.go' }, diagnostics: [] },
      }));

      expect(received[0]).toHaveLength(1);
      expect(received[1]).toHaveLength(1);
      expect(received[2]).toHaveLength(1);
    });

    it('broadcasts progress notifications to all channels', () => {
      dispatchMessage(channels, JSON.stringify({
        jsonrpc: '2.0',
        method: '$/progress',
        params: { token: 'tok1', value: { kind: 'report' } },
      }));

      expect(received[0]).toHaveLength(1);
      expect(received[1]).toHaveLength(1);
      expect(received[2]).toHaveLength(1);
    });

    it('delivers non-JSON data to all channels', () => {
      dispatchMessage(channels, 'not-json');

      expect(received[0]).toHaveLength(1);
      expect(received[0][0]).toBe('not-json');
    });
  });

  describe('notifications — multi-workspace', () => {
    beforeEach(() => setupMultiWorkspace(['/tmp/project-a', '/tmp/project-b', '/tmp/project-a']));

    it('scopes diagnostics to channels in the matching workspace', () => {
      dispatchMessage(channels, JSON.stringify({
        jsonrpc: '2.0',
        method: 'textDocument/publishDiagnostics',
        params: { textDocument: { uri: 'file:///tmp/project-a/main.go' }, diagnostics: [] },
      }));

      // Channels 1 and 3 are in project-a, channel 2 is in project-b
      expect(received[0]).toHaveLength(1); // project-a
      expect(received[1]).toHaveLength(0); // project-b — no match
      expect(received[2]).toHaveLength(1); // project-a
    });

    it('scopes diagnostics to the other workspace', () => {
      dispatchMessage(channels, JSON.stringify({
        jsonrpc: '2.0',
        method: 'textDocument/publishDiagnostics',
        params: { textDocument: { uri: 'file:///tmp/project-b/app.ts' }, diagnostics: [] },
      }));

      expect(received[0]).toHaveLength(0); // project-a — no match
      expect(received[1]).toHaveLength(1); // project-b
      expect(received[2]).toHaveLength(0); // project-a — no match
    });

    it('broadcasts global notifications to all channels regardless of workspace', () => {
      dispatchMessage(channels, JSON.stringify({
        jsonrpc: '2.0',
        method: 'window/logMessage',
        params: { type: 1, message: 'info' },
      }));

      expect(received[0]).toHaveLength(1);
      expect(received[1]).toHaveLength(1);
      expect(received[2]).toHaveLength(1);
    });

    it('broadcasts progress notifications to all channels', () => {
      dispatchMessage(channels, JSON.stringify({
        jsonrpc: '2.0',
        method: '$/progress',
        params: { token: 'tok1', value: { kind: 'report' } },
      }));

      expect(received[0]).toHaveLength(1);
      expect(received[1]).toHaveLength(1);
      expect(received[2]).toHaveLength(1);
    });
  });

  describe('response routing by _ch hint', () => {
    beforeEach(() => setup(3));

    it('routes to channel 1', () => {
      dispatchMessage(channels, JSON.stringify({
        jsonrpc: '2.0',
        id: 1001,
        _ch: 1,
        result: { items: [] },
      }));

      expect(received[0]).toHaveLength(1);
      const parsed = JSON.parse(received[0][0]);
      expect(parsed.id).toBe(1001);
      expect(parsed._ch).toBeUndefined(); // stripped
      expect(parsed.result).toEqual({ items: [] });
      expect(received[1]).toHaveLength(0);
      expect(received[2]).toHaveLength(0);
    });

    it('routes to channel 2', () => {
      dispatchMessage(channels, JSON.stringify({
        jsonrpc: '2.0',
        id: 2001,
        _ch: 2,
        result: { items: ['a'] },
      }));

      expect(received[0]).toHaveLength(0);
      expect(received[1]).toHaveLength(1);
      expect(received[2]).toHaveLength(0);
    });

    it('routes to channel 3', () => {
      dispatchMessage(channels, JSON.stringify({
        jsonrpc: '2.0',
        id: 3001,
        _ch: 3,
        result: null,
      }));

      expect(received[0]).toHaveLength(0);
      expect(received[1]).toHaveLength(0);
      expect(received[2]).toHaveLength(1);
    });

    it('drops response for unknown channel', () => {
      dispatchMessage(channels, JSON.stringify({
        jsonrpc: '2.0',
        id: 9999,
        _ch: 99,
        result: {},
      }));

      expect(received[0]).toHaveLength(0);
      expect(received[1]).toHaveLength(0);
      expect(received[2]).toHaveLength(0);
    });
  });

  describe('response routing by ID range (fallback)', () => {
    beforeEach(() => setup(3));

    it('routes ID 1500 to channel 1 (range 1001–1999)', () => {
      dispatchMessage(channels, JSON.stringify({
        jsonrpc: '2.0',
        id: 1500,
        result: {},
      }));

      expect(received[0]).toHaveLength(1);
      expect(received[1]).toHaveLength(0);
      expect(received[2]).toHaveLength(0);
    });

    it('routes ID 2500 to channel 2 (range 2001–2999)', () => {
      dispatchMessage(channels, JSON.stringify({
        jsonrpc: '2.0',
        id: 2500,
        result: {},
      }));

      expect(received[0]).toHaveLength(0);
      expect(received[1]).toHaveLength(1);
      expect(received[2]).toHaveLength(0);
    });

    it('routes ID 3001 to channel 3 (range 3001–3999)', () => {
      dispatchMessage(channels, JSON.stringify({
        jsonrpc: '2.0',
        id: 3001,
        result: {},
      }));

      expect(received[0]).toHaveLength(0);
      expect(received[1]).toHaveLength(0);
      expect(received[2]).toHaveLength(1);
    });

    it('does not route ID 500 (below all ranges)', () => {
      dispatchMessage(channels, JSON.stringify({
        jsonrpc: '2.0',
        id: 500,
        result: {},
      }));

      expect(received[0]).toHaveLength(0);
      expect(received[1]).toHaveLength(0);
      expect(received[2]).toHaveLength(0);
    });

    it('does not route ID 1000 (boundary — not in any range)', () => {
      dispatchMessage(channels, JSON.stringify({
        jsonrpc: '2.0',
        id: 1000,
        result: {},
      }));

      expect(received[0]).toHaveLength(0);
      expect(received[1]).toHaveLength(0);
      expect(received[2]).toHaveLength(0);
    });

    it('does not route ID 2000 (boundary)', () => {
      dispatchMessage(channels, JSON.stringify({
        jsonrpc: '2.0',
        id: 2000,
        result: {},
      }));

      expect(received[0]).toHaveLength(0);
      expect(received[1]).toHaveLength(0);
      expect(received[2]).toHaveLength(0);
    });
  });

  describe('_ch hint takes precedence over ID range', () => {
    beforeEach(() => setup(3));

    it('routes ID 1500 with _ch:2 to channel 2 (not channel 1)', () => {
      dispatchMessage(channels, JSON.stringify({
        jsonrpc: '2.0',
        id: 1500,
        _ch: 2,
        result: {},
      }));

      expect(received[0]).toHaveLength(0);
      expect(received[1]).toHaveLength(1);
      expect(received[2]).toHaveLength(0);
    });
  });

  describe('channel lifecycle', () => {
    it('handles empty channel list', () => {
      dispatchMessage([], JSON.stringify({ jsonrpc: '2.0', method: 'test' }));
      // No error
    });

    it('handles channel with null handler', () => {
      const ch: Channel = { id: 1, workspace: '/tmp/test', handler: null };
      dispatchMessage([ch], JSON.stringify({ jsonrpc: '2.0', method: 'test' }));
      // No error
    });

    it('handles removed channels', () => {
      setup(3);
      // Simulate removing channel 2
      channels[1].handler = null;

      dispatchMessage(channels, JSON.stringify({
        jsonrpc: '2.0',
        method: 'textDocument/publishDiagnostics',
        params: { textDocument: { uri: 'file:///tmp/project/main.go' }, diagnostics: [] },
      }));

      expect(received[0]).toHaveLength(1);
      expect(received[1]).toHaveLength(0); // removed
      expect(received[2]).toHaveLength(1);
    });
  });

  describe('error responses', () => {
    beforeEach(() => setup(2));

    it('routes error response by _ch', () => {
      dispatchMessage(channels, JSON.stringify({
        jsonrpc: '2.0',
        id: 1001,
        _ch: 1,
        error: { code: -32603, message: 'Internal error' },
      }));

      expect(received[0]).toHaveLength(1);
      const parsed = JSON.parse(received[0][0]);
      expect(parsed.error).toEqual({ code: -32603, message: 'Internal error' });
      expect(received[1]).toHaveLength(0);
    });
  });

  describe('$server/lspReady notification', () => {
    beforeEach(() => setup(3));

    it('calls onReady on targeted channel when _ch hint is present', () => {
      let readyCalls: number[] = []; // channel IDs that received onReady
      for (let i = 0; i < channels.length; i++) {
        const idx = i;
        channels[i].onReady = () => readyCalls.push(channels[i].id);
      }

      dispatchMessage(channels, JSON.stringify({
        jsonrpc: '2.0',
        method: '$/lspReady',
        _ch: 2,
      }));

      expect(readyCalls).toEqual([2]); // only channel 2
    });

    it('broadcasts onReady to all channels when no _ch hint', () => {
      let readyCalls: number[] = [];
      for (let i = 0; i < channels.length; i++) {
        const idx = i;
        channels[i].onReady = () => readyCalls.push(channels[i].id);
      }

      dispatchMessage(channels, JSON.stringify({
        jsonrpc: '2.0',
        method: '$/lspReady',
      }));

      expect(readyCalls).toEqual([1, 2, 3]); // all channels
    });

    it('does not deliver $server/lspReady to handler (not a normal LSP message)', () => {
      dispatchMessage(channels, JSON.stringify({
        jsonrpc: '2.0',
        method: '$/lspReady',
        _ch: 1,
      }));

      // Notification handlers should NOT receive this message
      expect(received[0]).toHaveLength(0);
      expect(received[1]).toHaveLength(0);
      expect(received[2]).toHaveLength(0);
    });

    it('ignores onReady for unknown channel', () => {
      let readyCalls: number[] = [];
      for (let i = 0; i < channels.length; i++) {
        channels[i].onReady = () => readyCalls.push(channels[i].id);
      }

      dispatchMessage(channels, JSON.stringify({
        jsonrpc: '2.0',
        method: '$/lspReady',
        _ch: 99, // unknown channel
      }));

      expect(readyCalls).toEqual([]); // no channel matched
    });
  });

  describe('serverInfo extraction', () => {
    it('calls onServerInfo with server name and version from initialize response', () => {
      setup(2);
      let serverInfo: { name: string; version: string | null } | null = null;
      channels[0].onServerInfo = (name, version) => {
        serverInfo = { name, version };
      };

      dispatchMessage(channels, JSON.stringify({
        jsonrpc: '2.0',
        id: 1,
        _ch: 1,
        result: {
          capabilities: { completionProvider: {} },
          serverInfo: { name: 'gopls', version: 'v0.15.0' },
        },
      }));

      expect(serverInfo).toEqual({ name: 'gopls', version: 'v0.15.0' });
      expect(received[0]).toHaveLength(1);
      const parsed = JSON.parse(received[0][0]);
      expect(parsed._ch).toBeUndefined(); // _ch stripped
      expect(parsed.result.serverInfo).toEqual({ name: 'gopls', version: 'v0.15.0' });
    });

    it('calls onServerInfo with null version when not provided', () => {
      setup(1);
      let serverInfo: { name: string; version: string | null } | null = null;
      channels[0].onServerInfo = (name, version) => {
        serverInfo = { name, version };
      };

      dispatchMessage(channels, JSON.stringify({
        jsonrpc: '2.0',
        id: 1,
        _ch: 1,
        result: {
          capabilities: {},
          serverInfo: { name: 'ts-lsp' },
        },
      }));

      expect(serverInfo).toEqual({ name: 'ts-lsp', version: null });
    });

    it('does not call onServerInfo for non-initialize responses', () => {
      setup(1);
      let called = false;
      channels[0].onServerInfo = () => { called = true; };

      dispatchMessage(channels, JSON.stringify({
        jsonrpc: '2.0',
        id: 2,
        _ch: 1,
        result: { items: [] },
      }));

      expect(called).toBe(false);
    });

    it('calls onServerInfo via ID range fallback', () => {
      setup(2);
      let serverInfo: { name: string; version: string | null } | null = null;
      channels[0].onServerInfo = (name, version) => {
        serverInfo = { name, version };
      };

      dispatchMessage(channels, JSON.stringify({
        jsonrpc: '2.0',
        id: 1001, // in channel 1's range (1001-1999)
        result: {
          capabilities: {},
          serverInfo: { name: 'rust-analyzer', version: '1.75.0' },
        },
      }));

      expect(serverInfo).toEqual({ name: 'rust-analyzer', version: '1.75.0' });
    });
  });
});
