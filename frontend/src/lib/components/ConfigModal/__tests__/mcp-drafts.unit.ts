import { describe, it, expect } from 'vitest';
import {
    buildMCPServers,
    mcpServerPayload,
    mcpServersPayload,
    newMCPServerDraft,
    validateMCPServers,
} from '../mcp-drafts';
import { MASKED_SECRET } from '$lib/api/helpers';
import type { MCPServer } from '$lib/types';

// The registry is saved whole, so a round trip through the form must lose nothing a person stored, above
// all a secret they did not touch. The refusals name the field they refuse.

const draftOf = (server: MCPServer) => buildMCPServers({ mcp_servers: [server] })[0];

const stored: MCPServer = {
    id: 'a1b2c3d4e5f60708',
    name: 'SearXNG',
    url: 'https://mcp.example.com/mcp',
    enabled: true,
    timeout_seconds: 20,
    headers: [{ name: 'Authorization', value: MASKED_SECRET }],
};

describe('buildMCPServerDraft', () => {
    it('reads a masked header as a stored value rather than as text to send', () => {
        const draft = draftOf(stored);
        expect(draft.headers[0].stored).toBe(true);
        // The mask never reaches the field: typing after it would store a token nobody typed.
        expect(draft.headers[0].value).toBe('');
        expect(draft.id).toBe(stored.id);
        expect(draft.timeoutSeconds).toBe(20);
    });

    it('gives an unsaved row a key of its own, so {#each} survives a rename', () => {
        const draft = newMCPServerDraft('mcp-new-1');
        expect(draft.id).toBe('');
        expect(draft.uid).toBe('mcp-new-1');
        expect(draft.enabled).toBe(true);
    });
});

describe('mcpServerPayload', () => {
    it('sends the masked placeholder back, which is what keeps the stored token', () => {
        const payload = mcpServerPayload(draftOf(stored));
        expect(payload.headers).toEqual([{ name: 'Authorization', value: MASKED_SECRET }]);
    });

    it('sends what was typed in place of a stored value, and nothing of the placeholder with it', () => {
        const draft = draftOf(stored);
        draft.headers[0].value = 'Bearer new-token';
        expect(mcpServerPayload(draft).headers).toEqual([{ name: 'Authorization', value: 'Bearer new-token' }]);
    });

    it('keeps a stored token whose field was left empty, and removes the header by dropping the row', () => {
        const draft = draftOf(stored);
        draft.headers[0].value = '';
        expect(mcpServerPayload(draft).headers).toEqual([{ name: 'Authorization', value: MASKED_SECRET }]);
        draft.headers = [];
        expect(mcpServerPayload(draft).headers).toBeUndefined();
    });

    it('sends the id and the deadline only when the row has them', () => {
        const fresh = mcpServerPayload(newMCPServerDraft('mcp-new-2'));
        expect(fresh.id).toBeUndefined();
        expect(fresh.timeout_seconds).toBeUndefined();

        const saved = mcpServerPayload(draftOf(stored));
        expect(saved.id).toBe(stored.id);
        expect(saved.timeout_seconds).toBe(20);
    });

    it('trims the address and drops the trailing slash the browser likes to add', () => {
        const draft = draftOf({ ...stored, url: '  https://mcp.example.com/mcp/  ' });
        expect(mcpServerPayload(draft).url).toBe('https://mcp.example.com/mcp');
    });

    it('drops a header row that carries neither a name nor a value', () => {
        const draft = draftOf(stored);
        draft.headers.push({ name: '   ', value: '', stored: false });
        expect(mcpServerPayload(draft).headers).toHaveLength(1);
    });
});

describe('mcpServersPayload', () => {
    it('leaves an untouched new row out of the save entirely', () => {
        const drafts = [draftOf(stored), newMCPServerDraft('mcp-new-3')];
        expect(mcpServersPayload(drafts)).toHaveLength(1);
    });

    it('round-trips a saved registry unchanged, which is what a whole-array write demands', () => {
        const config = { mcp_servers: [stored] };
        expect(mcpServersPayload(buildMCPServers(config))).toEqual([stored]);
    });
});

describe('validateMCPServers', () => {
    const named = (over: Partial<ReturnType<typeof newMCPServerDraft>> = {}) => ({
        ...newMCPServerDraft('uid-1'),
        name: 'SearXNG',
        url: 'https://mcp.example.com/mcp',
        ...over,
    });
    const key = (drafts: ReturnType<typeof newMCPServerDraft>[]) => validateMCPServers(drafts)?.key;

    it('accepts a registry that is complete', () => {
        expect(validateMCPServers([named()])).toBeNull();
        expect(validateMCPServers(buildMCPServers({ mcp_servers: [stored] }))).toBeNull();
    });

    it('refuses the shapes the save refuses, naming each field', () => {
        expect(key([{ ...named(), name: '  ' }])).toBe('settings.mcp.validate.noName');
        expect(key([{ ...named(), url: '' }])).toBe('settings.mcp.validate.noUrl');
        expect(key([{ ...named(), url: 'stdio://nothing' }])).toBe('settings.mcp.validate.transport');
        expect(key([{ ...named(), timeoutSeconds: 9999 }])).toBe('settings.mcp.validate.timeout');
        expect(key([{ ...named(), headers: [{ name: 'X Bad', value: 'v', stored: false }] }])).toBe(
            'settings.mcp.validate.headerName'
        );
        expect(key([{ ...named(), headers: [{ name: 'X-Bad\r\nX-Other', value: 'v', stored: false }] }])).toBe(
            'settings.mcp.validate.headerName'
        );
        expect(key([{ ...named(), headers: [{ name: '', value: 'v', stored: false }] }])).toBe(
            'settings.mcp.validate.headerNoName'
        );
        expect(
            key([
                {
                    ...named(),
                    headers: [
                        { name: 'Authorization', value: 'a', stored: false },
                        { name: 'authorization', value: 'b', stored: false },
                    ],
                },
            ])
        ).toBe('settings.mcp.validate.headerTwice');
    });

    it('refuses two names whose tools would collide, and says which two', () => {
        // The names differ; the slug the tools are named after does not. Same pair the save refuses,
        // under the same slug, because both halves of the rule must keep agreeing.
        const problem = validateMCPServers([named({ name: 'my server' }), named({ uid: 'uid-2', name: 'My-Server' })]);
        expect(problem?.key).toBe('settings.mcp.validate.slugClash');
        expect(problem?.params).toMatchObject({ other: 'my server', name: 'My-Server', slug: 'my-server' });
    });
});
