import { describe, it, expect, beforeEach } from 'vitest';
import { isLSPAvailable, SharedLSPConnection } from '$lib/utils/lsp-connection-manager';
import { extensionManager } from '$lib/services/extension-manager.svelte';
import type { Extension } from '$lib/types';

/**
 * Tests for the LSP connection manager's message routing.
 *
 * They drive the real SharedLSPConnection.dispatchMessage — it reads no socket, so no WebSocket
 * mocking is needed (jsdom's is unreliable anyway). Connection lifecycle is covered by the backend
 * handler tests.
 */

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
            version: '1.0.0',
            capabilities: {
                lsp: {
                    command: name,
                    args: [],
                    // A declared file type per suffix, which is what the backend and the editor
                    // both route on now.
                    file_types: extensions.map((e) => ({ extension: e, language_id: language })),
                    language,
                },
            },
        },
        config: {},
        container_config: {},
        sandbox_config: { network_isolation: true, memory_limit: 256 },
        install_source: 'manual',
        enabled: true,
        running: false,
        installed_at: new Date().toISOString(),
        built_at: new Date().toISOString(),
    };
    extensionManager.state.extensions.push(ext);
}

/**
 * The gate that decides whether the editor opens an LSP channel or keeps CodeMirror's completion,
 * read from the same declaration as the grammar and the wire id.
 */
describe('isLSPAvailable', () => {
  beforeEach(() => {
    // Reset extensions before each test
    extensionManager.state.extensions = [];
  });

  it('is false for every file while nothing is installed', () => {
    expect(isLSPAvailable('main.go')).toBe(false);
    expect(isLSPAvailable('config.toml')).toBe(false);
  });

  it('is true only for the suffixes the installed extension declares', () => {
    addMockLSPExt('gopls', 'go', ['go']);
    expect(isLSPAvailable('main.go')).toBe(true);
    expect(isLSPAvailable('config.toml')).toBe(false);
    expect(isLSPAvailable('README.md')).toBe(false);
  });

  it('covers every file type one extension owns, not just its first', () => {
    addMockLSPExt('typescript-language-server', 'typescript', ['ts', 'tsx', 'js', 'jsx', 'mjs', 'cjs']);
    for (const file of ['app.ts', 'App.tsx', 'app.js', 'app.jsx', 'app.mjs', 'app.cjs']) {
      expect(isLSPAvailable(file), file).toBe(true);
    }
  });

  it('is false for a file an extension owns but the user hid from the editor', () => {
    addMockLSPExt('gopls', 'go', ['go']);
    extensionManager.state.extensions[0].manifest.capabilities!.lsp!.editor_enabled = false;
    expect(isLSPAvailable('main.go')).toBe(false);
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
 * The routing rules live in SharedLSPConnection.dispatchMessage, and the cast is only because the
 * members are labelled private: dispatch reads no socket, so a connection built here is enough to
 * drive it. A copy of the routing in this file would pin nothing — it did, until it was replaced.
 */
function dispatchMessage(channels: Channel[], data: string): void {
  const mgr = new SharedLSPConnection();
  const internals = mgr as unknown as {
    channels: Map<number, Channel>;
    dispatchMessage(data: unknown): void;
  };
  internals.channels = new Map(channels.map((ch) => [ch.id, ch]));
  internals.dispatchMessage(data);
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
      // The branch is "no textDocument.uri", so any method without one takes this path — `$/progress`
      // included, which the single-workspace describe pins by name.
      dispatchMessage(channels, JSON.stringify({
        jsonrpc: '2.0',
        method: 'window/logMessage',
        params: { type: 1, message: 'info' },
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
