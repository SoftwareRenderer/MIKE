import { describe, it, expect, beforeEach } from 'vitest';
import { detectLanguage } from '$lib/utils/lsp-connection-manager';

/**
 * Tests for the LSP connection manager's message routing logic.
 *
 * The routing logic is extracted here for direct unit testing without
 * WebSocket mocking (jsdom WebSocket mocking is unreliable).
 * Connection lifecycle tests are covered by the backend handler tests.
 */

const CHANNEL_ID_STEP = 1000;

describe('detectLanguage', () => {
  it('maps Go extensions to go', () => {
    expect(detectLanguage('main.go')).toBe('go');
  });

  it('maps TypeScript extensions to typescript', () => {
    expect(detectLanguage('app.ts')).toBe('typescript');
    expect(detectLanguage('app.tsx')).toBe('typescript');
  });

  it('maps JavaScript extensions to typescript', () => {
    expect(detectLanguage('app.js')).toBe('typescript');
    expect(detectLanguage('app.jsx')).toBe('typescript');
    expect(detectLanguage('app.mjs')).toBe('typescript');
    expect(detectLanguage('app.cjs')).toBe('typescript');
  });

  it('maps Python extensions to python', () => {
    expect(detectLanguage('main.py')).toBe('python');
    expect(detectLanguage('stub.pyi')).toBe('python');
  });

  it('maps Rust extensions to rust', () => {
    expect(detectLanguage('main.rs')).toBe('rust');
  });

  it('falls back to lowercase extension for unknown types', () => {
    expect(detectLanguage('config.toml')).toBe('toml');
    expect(detectLanguage('README.md')).toBe('md');
  });
});

interface Channel {
  id: number;
  handler: ((value: string) => void) | null;
  onServerInfo?: (name: string, version: string | null) => void;
}

/**
 * Simulates the dispatchMessage logic from WorkspaceConnection.
 * This is the core routing logic that determines which channel gets which message.
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

  // Notification (no id, has method) → broadcast to ALL channels
  if (parsed.id == null && parsed.method) {
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

  function setup(channelCount: number) {
    channels = [];
    received = [];
    for (let i = 1; i <= channelCount; i++) {
      channels.push({ id: i, handler: null });
      received.push([]);
    }
    // Wire up handlers
    for (let i = 0; i < channels.length; i++) {
      const idx = i;
      channels[i].handler = (msg) => received[idx].push(msg);
    }
  }

  describe('notifications', () => {
    beforeEach(() => setup(3));

    it('broadcasts to all channels', () => {
      dispatchMessage(channels, JSON.stringify({
        jsonrpc: '2.0',
        method: 'textDocument/publishDiagnostics',
        params: { uri: 'file:///main.go' },
      }));

      expect(received[0]).toHaveLength(1);
      expect(received[1]).toHaveLength(1);
      expect(received[2]).toHaveLength(1);
    });

    it('broadcasts progress notifications', () => {
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
      const ch: Channel = { id: 1, handler: null };
      dispatchMessage([ch], JSON.stringify({ jsonrpc: '2.0', method: 'test' }));
      // No error
    });

    it('handles removed channels', () => {
      setup(3);
      // Simulate removing channel 2
      channels[1].handler = null;

      dispatchMessage(channels, JSON.stringify({
        jsonrpc: '2.0',
        method: 'test',
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
