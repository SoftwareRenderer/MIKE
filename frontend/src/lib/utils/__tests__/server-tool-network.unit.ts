import { describe, it, expect } from 'vitest';
import { formatNetworkedExtensions, serverToolExtensions } from '../server-tool-network';

const searxng = { name: 'contrib-searxng', display_name: 'SearXNG Search', tools: ['web_search'] };
const other = { name: 'contrib-other', display_name: 'Other API', tools: ['lookup', 'fetch'] };

describe('serverToolExtensions', () => {
  it('says nothing when the project is not sandboxed at all', () => {
    const config = { server_network_extensions: [searxng] };
    expect(serverToolExtensions(config, false, true)).toEqual([]);
    expect(serverToolExtensions(config, false, false)).toEqual([]);
  });

  it('says nothing while isolation is off — an open network needs no explaining', () => {
    expect(serverToolExtensions({ server_network_extensions: [searxng] }, true, false)).toEqual([]);
  });

  it('names the extensions when isolation is on and one contributes a server-side tool', () => {
    expect(serverToolExtensions({ server_network_extensions: [searxng, other] }, true, true)).toEqual([
      searxng,
      other,
    ]);
  });

  it('says nothing when no extension contributes one, or the config has not arrived', () => {
    expect(serverToolExtensions({ server_network_extensions: [] }, true, true)).toEqual([]);
    expect(serverToolExtensions({ server_network_extensions: undefined }, true, true)).toEqual([]);
    expect(serverToolExtensions(null, true, true)).toEqual([]);
    expect(serverToolExtensions(undefined, true, true)).toEqual([]);
  });
});

describe('formatNetworkedExtensions', () => {
  it('names each extension with the tools it contributes', () => {
    expect(formatNetworkedExtensions([searxng])).toBe('SearXNG Search (web_search)');
    expect(formatNetworkedExtensions([searxng, other])).toBe(
      'SearXNG Search (web_search), Other API (lookup, fetch)'
    );
  });

  it('drops the parentheses when an extension names no tool', () => {
    expect(formatNetworkedExtensions([{ name: 'x', display_name: 'X', tools: [] }])).toBe('X');
  });
});
