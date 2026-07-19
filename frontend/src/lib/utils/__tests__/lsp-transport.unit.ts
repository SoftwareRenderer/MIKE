import { describe, it, expect, beforeEach, afterEach } from 'vitest';
import { createLSPTransport } from '$lib/utils/lsp-transport';

interface MockResult {
  ctor: typeof WebSocket;
  capturedUrl: () => string | null;
  lastMock: () => any;
}

/** Build a mock WebSocket constructor for use in tests. */
function makeMockCtor(shouldFail: boolean = false): MockResult {
  let capturedUrl: string | null = null;
  let lastMock: any = null;

  function MockWs(this: any, url: string) {
    capturedUrl = url;
    this.url = url;
    this.readyState = 1; // OPEN
    this.onopen = null;
    this.onerror = null;
    this.onclose = null;
    this.onmessage = null;
    this._sent = [] as string[];

    const self = this;
    this.send = function (data: string) {
      self._sent.push(data);
    };
    this.getSentMessages = function (): string[] {
      return [...self._sent];
    };

    lastMock = this;

    // Schedule onopen or onerror asynchronously (mimics real WebSocket)
    if (shouldFail) {
      queueMicrotask(() => {
        if (self.onerror) self.onerror(new Event('error'));
      });
    } else {
      queueMicrotask(() => {
        if (self.onopen) self.onopen();
      });
    }
  }

  // Set static readonly properties via Object.defineProperty
  Object.defineProperty(MockWs, 'CONNECTING', { value: 0 });
  Object.defineProperty(MockWs, 'OPEN', { value: 1 });
  Object.defineProperty(MockWs, 'CLOSING', { value: 2 });
  Object.defineProperty(MockWs, 'CLOSED', { value: 3 });

  return {
    ctor: MockWs as unknown as typeof WebSocket,
    capturedUrl: () => capturedUrl,
    lastMock: () => lastMock,
  };
}

describe('createLSPTransport', () => {
  let originalWebSocket: typeof WebSocket;
  let capturedUrlGetter: () => string | null;
  let lastMockGetter: () => any;

  function setupMock(shouldFail: boolean = false) {
    const result = makeMockCtor(shouldFail);
    capturedUrlGetter = result.capturedUrl;
    lastMockGetter = result.lastMock;
    globalThis.WebSocket = result.ctor;
  }

  beforeEach(() => {
    originalWebSocket = globalThis.WebSocket;
    setupMock();
    Object.defineProperty(globalThis, 'location', {
      value: { protocol: 'http:', host: 'localhost:3000' },
      writable: true,
      configurable: true,
    });
  });

  afterEach(() => {
    globalThis.WebSocket = originalWebSocket;
  });

  describe('WebSocket URL construction', () => {
    it('uses ws:// for http protocol', async () => {
      await createLSPTransport({
        workspace: '/home/user/project',
        filePath: '/home/user/project/main.go',
      });

      expect(capturedUrlGetter()).toBe('ws://localhost:3000/api/lsp');
    });

    it('uses wss:// for https protocol', async () => {
      Object.defineProperty(globalThis, 'location', {
        value: { protocol: 'https:', host: 'example.com' },
        writable: true,
        configurable: true,
      });
      setupMock();

      await createLSPTransport({
        workspace: '/home/user/project',
        filePath: '/home/user/project/main.go',
      });

      expect(capturedUrlGetter()).toBe('wss://example.com/api/lsp');
    });
  });

  describe('workspace/subscribe message', () => {
    it('sends subscribe message on connect', async () => {
      await createLSPTransport({
        workspace: '/workspace/myproject',
        filePath: '/workspace/myproject/src/index.ts',
      });

      const mock = lastMockGetter();
      const messages = mock.getSentMessages();
      expect(messages).toHaveLength(1);
      const parsed = JSON.parse(messages[0]);
      expect(parsed.method).toBe('workspace/subscribe');
      expect(parsed.params.workspace).toBe('/workspace/myproject');
      expect(parsed.params.file).toBe('/workspace/myproject/src/index.ts');
    });
  });

  describe('Transport send', () => {
    it('forwards messages to WebSocket', async () => {
      const transport = await createLSPTransport({
        workspace: '/home/user/project',
        filePath: '/home/user/project/main.go',
      });

      const mock = lastMockGetter();
      transport.send('{"jsonrpc":"2.0","method":"initialize"}');

      const messages = mock.getSentMessages();
      // First message is the subscribe, second is our send
      expect(messages).toHaveLength(2);
      expect(messages[1]).toBe('{"jsonrpc":"2.0","method":"initialize"}');
    });

    it('does not send when WebSocket is not OPEN', async () => {
      const transport = await createLSPTransport({
        workspace: '/home/user/project',
        filePath: '/home/user/project/main.go',
      });

      const mock = lastMockGetter();
      mock.readyState = 3; // CLOSED
      transport.send('should not send');

      const messages = mock.getSentMessages();
      expect(messages).toHaveLength(1); // only the subscribe
    });
  });

  describe('Transport subscribe', () => {
    it('receives incoming messages via callback', async () => {
      const transport = await createLSPTransport({
        workspace: '/home/user/project',
        filePath: '/home/user/project/main.go',
      });

      const received: string[] = [];
      transport.subscribe((msg) => received.push(msg));

      const mock = lastMockGetter();
      mock.onmessage(new MessageEvent('message', { data: '{"jsonrpc":"2.0","result":{}}' }));
      mock.onmessage(new MessageEvent('message', { data: '{"jsonrpc":"2.0","method":"textDocument/publishDiagnostics"}' }));

      expect(received).toHaveLength(2);
      expect(received[0]).toBe('{"jsonrpc":"2.0","result":{}}');
      expect(received[1]).toBe('{"jsonrpc":"2.0","method":"textDocument/publishDiagnostics"}');
    });

    it('decodes ArrayBuffer data', async () => {
      const transport = await createLSPTransport({
        workspace: '/home/user/project',
        filePath: '/home/user/project/main.go',
      });

      const received: string[] = [];
      transport.subscribe((msg) => received.push(msg));

      const mock = lastMockGetter();
      const encoder = new TextEncoder();
      const buffer = encoder.encode('{"jsonrpc":"2.0"}');
      mock.onmessage(new MessageEvent('message', { data: buffer }));

      expect(received).toHaveLength(1);
      expect(received[0]).toBe('{"jsonrpc":"2.0"}');
    });
  });

  describe('Transport unsubscribe', () => {
    it('stops receiving messages after unsubscribe', async () => {
      const transport = await createLSPTransport({
        workspace: '/home/user/project',
        filePath: '/home/user/project/main.go',
      });

      const received: string[] = [];
      const handler = (msg: string) => received.push(msg);
      transport.subscribe(handler);

      const mock = lastMockGetter();
      mock.onmessage(new MessageEvent('message', { data: 'before-unsubscribe' }));
      expect(received).toHaveLength(1);

      transport.unsubscribe(handler);
      // After unsubscribe, onmessage is set to null
      expect(mock.onmessage).toBeNull();
    });
  });

  describe('Connection callbacks', () => {
    it('calls onConnected when WebSocket opens', async () => {
      let connected = false;
      await createLSPTransport({
        workspace: '/home/user/project',
        filePath: '/home/user/project/main.go',
        onConnected: () => { connected = true; },
      });

      expect(connected).toBe(true);
    });

    it('calls onDisconnected when WebSocket closes', async () => {
      let disconnected = false;
      await createLSPTransport({
        workspace: '/home/user/project',
        filePath: '/home/user/project/main.go',
        onDisconnected: () => { disconnected = true; },
      });

      const mock = lastMockGetter();
      mock.onclose();

      expect(disconnected).toBe(true);
    });
  });

  describe('Error handling', () => {
    it('rejects on WebSocket error', async () => {
      setupMock(true);

      await expect(createLSPTransport({
        workspace: '/home/user/project',
        filePath: '/home/user/project/main.go',
      })).rejects.toThrow('LSP WebSocket error');
    });
  });
});
