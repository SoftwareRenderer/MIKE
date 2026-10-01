import { describe, it, expect } from 'vitest';
import { enabledMCPServerNames, projectMCPServerIDs, selectableMCPServers } from '../mcp-project';
import type { Config } from '$lib/types';

// A project's opt-in is stored as ids and read back as names, and both the save payload and the Sandbox
// Configuration's sentence are built in the registry's order.

const config: Pick<Config, 'mcp_servers'> = {
    mcp_servers: [
        { id: 'aa', name: 'SearXNG', url: 'https://a.example.com/mcp', enabled: true },
        { id: 'bb', name: 'Files', url: 'https://b.example.com/mcp', enabled: false },
        { id: 'cc', name: 'Linear', url: 'https://c.example.com/mcp', enabled: true },
    ],
};

describe('selectableMCPServers', () => {
    it('offers only the globally enabled servers', () => {
        expect(selectableMCPServers(config).map((s) => s.id)).toEqual(['aa', 'cc']);
    });

    it('offers nothing when no config has arrived', () => {
        expect(selectableMCPServers(null)).toEqual([]);
        expect(selectableMCPServers(undefined)).toEqual([]);
    });
});

describe('projectMCPServerIDs', () => {
    it('sends the selection in the registry order, whichever order it was clicked in', () => {
        expect(projectMCPServerIDs(config, ['cc', 'aa'])).toEqual(['aa', 'cc']);
        expect(projectMCPServerIDs(config, new Set(['cc']))).toEqual(['cc']);
    });

    it('drops an id no registered server is behind, so a deleted server cannot be re-sent', () => {
        expect(projectMCPServerIDs(config, ['aa', 'gone'])).toEqual(['aa']);
    });
});

describe('enabledMCPServerNames', () => {
    it('names what the project enabled, in the registry order', () => {
        expect(enabledMCPServerNames(config, { mcp_server_ids: ['cc', 'aa'] })).toEqual(['SearXNG', 'Linear']);
    });

    it('names nothing a server that was disabled globally, or a project that chose nothing', () => {
        expect(enabledMCPServerNames(config, { mcp_server_ids: ['bb'] })).toEqual([]);
        expect(enabledMCPServerNames(config, { mcp_server_ids: [] })).toEqual([]);
        expect(enabledMCPServerNames(config, null)).toEqual([]);
    });

    it('is inert about an id with no server behind it — deleting one is not an error', () => {
        expect(enabledMCPServerNames(config, { mcp_server_ids: ['gone', 'aa'] })).toEqual(['SearXNG']);
    });
});
