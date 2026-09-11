import { describe, it, expect } from 'vitest';
import { StreamState } from '$lib/services/stream-state.svelte';
import type { ChatEntry } from '$lib/types';

describe('StreamState', () => {
    it('starts with empty state', () => {
        const s = new StreamState();
        expect(s.state.entries).toEqual([]);
        expect(s.state.currentAssistantId).toBeNull();
        expect(s.state.awaitingResponse).toBe(false);
        expect(s.state.queuePosition).toBe(0);
        expect(s.state.queueEstimatedWait).toBe(0);
        expect(s.state.streamTokenCount).toBe(0);
        expect(s.state.lastPromptTokens).toBe(0);
        expect(s.state.activeToolCalls.size).toBe(0);
        expect(s.state.processedToolIds.size).toBe(0);
        expect(s.state.pendingCheckpoints.size).toBe(0);
        expect(s.state.checkpointLabels.size).toBe(0);
        expect(s.state.checkpointCommitHashes.size).toBe(0);
    });

    it('ensureAssistantEntry creates a new entry with correct defaults', () => {
        const s = new StreamState();
        const entry = s.ensureAssistantEntry(() => 'gen-1');
        expect(entry.id).toBe('gen-1');
        expect(entry.role).toBe('assistant');
        expect(entry.content).toBe('');
        expect(entry.isStreaming).toBe(true);
        expect(entry.createdAt).toBeDefined();
        expect(s.state.entries.length).toBe(1);
        expect(s.state.currentAssistantId).toBe('gen-1');
    });

    it('ensureAssistantEntry returns existing entry when currentAssistantId is set', () => {
        const s = new StreamState();
        const first = s.ensureAssistantEntry(() => 'gen-1');
        const second = s.ensureAssistantEntry(() => 'gen-2');
        // Both should refer to the same entry (same id, same content)
        expect(first.id).toBe(second.id);
        expect(first.id).toBe('gen-1');
        expect(s.state.entries.length).toBe(1);
        expect(s.state.currentAssistantId).toBe('gen-1');
    });

    it('ensureAssistantEntry creates a new entry after closeCurrentAssistant', () => {
        const s = new StreamState();
        s.ensureAssistantEntry(() => 'gen-1');
        s.closeCurrentAssistant();
        // Now currentAssistantId is null, so a new entry should be created
        const entry = s.ensureAssistantEntry(() => 'gen-2');
        expect(entry.id).toBe('gen-2');
        expect(s.state.entries.length).toBe(2);
    });

    it('ensureAssistantEntry creates new entry when previous one was removed from entries', () => {
        const s = new StreamState();
        s.ensureAssistantEntry(() => 'gen-1');
        // Simulate entry being removed (e.g., during resetStreaming)
        s.state.entries = [];
        // currentAssistantId is still set but entry is gone
        const entry = s.ensureAssistantEntry(() => 'gen-2');
        expect(entry.id).toBe('gen-2');
        expect(s.state.entries.length).toBe(1);
    });

    it('closeCurrentAssistant sets isStreaming to false and clears currentAssistantId', () => {
        const s = new StreamState();
        s.ensureAssistantEntry(() => 'test-id');
        expect(s.state.entries[0].isStreaming).toBe(true);
        expect(s.state.currentAssistantId).toBe('test-id');

        s.closeCurrentAssistant();
        expect(s.state.entries[0].isStreaming).toBe(false);
        expect(s.state.currentAssistantId).toBeNull();
    });

    it('closeCurrentAssistant is safe when no current assistant', () => {
        const s = new StreamState();
        expect(() => s.closeCurrentAssistant()).not.toThrow();
        expect(s.state.entries).toEqual([]);
    });

    it('closeCurrentAssistant is safe when entry is missing', () => {
        const s = new StreamState();
        s.ensureAssistantEntry(() => 'test-id');
        s.state.entries = []; // entry removed but id still set
        expect(() => s.closeCurrentAssistant()).not.toThrow();
        expect(s.state.currentAssistantId).toBeNull();
    });

    it('clearAwaiting sets awaitingResponse to false and resets promptProgress', () => {
        const s = new StreamState();
        s.state.awaitingResponse = true;
        s.state.promptProgress = 75;
        s.clearAwaiting();
        expect(s.state.awaitingResponse).toBe(false);
        expect(s.state.promptProgress).toBe(0);
    });

    it('clearAwaiting also resets retry state', () => {
        const s = new StreamState();
        s.state.retryCount = 3;
        s.state.retryError = 'connection refused';
        s.clearAwaiting();
        expect(s.state.retryCount).toBe(0);
        expect(s.state.retryError).toBe('');
    });

    it('clearAwaiting is safe when already cleared', () => {
        const s = new StreamState();
        expect(() => s.clearAwaiting()).not.toThrow();
        expect(s.state.awaitingResponse).toBe(false);
    });

    it('clearRetry resets retry count and error', () => {
        const s = new StreamState();
        s.state.retryCount = 5;
        s.state.retryError = 'timeout';
        s.clearRetry();
        expect(s.state.retryCount).toBe(0);
        expect(s.state.retryError).toBe('');
        // Other state unaffected
        s.state.awaitingResponse = true;
        s.clearRetry();
        expect(s.state.awaitingResponse).toBe(true);
    });

    it('resetStreaming closes assistant and clears tool-related state', () => {
        const s = new StreamState();
        s.ensureAssistantEntry(() => 'test-id');
        s.state.activeToolCalls.set('tc1', { entryId: 'test-id', toolCall: { toolId: 'tc1', toolName: 'read', args: {} } });
        s.state.processedToolIds.add('tc1');
        s.state.streamTokenCount = 42;
        s.state.awaitingResponse = true;

        s.resetStreaming();
        expect(s.state.currentAssistantId).toBeNull();
        expect(s.state.activeToolCalls.size).toBe(0);
        expect(s.state.processedToolIds.size).toBe(0);
        expect(s.state.streamTokenCount).toBe(0);
        expect(s.state.awaitingResponse).toBe(false);
        // Entries should still exist (only streaming state is cleared)
        expect(s.state.entries.length).toBe(1);
    });

    it('reset clears all entries and state', () => {
        const s = new StreamState();
        s.ensureAssistantEntry(() => 'test-id');
        s.state.entries[0].content = 'Hello world';
        s.state.awaitingResponse = true;
        s.state.queuePosition = 5;
        s.state.queueEstimatedWait = 30;
        s.state.promptProgress = 50;
        s.state.retryCount = 3;
        s.state.retryError = 'connection refused';
        s.state.contextState = { used: 1000, limit: 4096 };
        s.state.lastPromptTokens = 500;
        s.state.streamTokenCount = 42;
        s.state.pendingCheckpoints.add('msg-1');
        s.state.checkpointLabels.set('msg-1', 'label');
        s.state.checkpointCommitHashes.set('msg-1', 'abc123');
        s.state.checkpointHashesVersion = 5;

        s.reset();
        expect(s.state.entries).toEqual([]);
        expect(s.state.currentAssistantId).toBeNull();
        expect(s.state.awaitingResponse).toBe(false);
        expect(s.state.queuePosition).toBe(0);
        expect(s.state.queueEstimatedWait).toBe(0);
        expect(s.state.promptProgress).toBe(0);
        expect(s.state.retryCount).toBe(0);
        expect(s.state.retryError).toBe('');
        expect(s.state.contextState).toEqual({ used: 0, limit: 0 });
        expect(s.state.lastPromptTokens).toBe(0);
        expect(s.state.streamTokenCount).toBe(0);
        expect(s.state.pendingCheckpoints.size).toBe(0);
        expect(s.state.checkpointLabels.size).toBe(0);
        expect(s.state.checkpointCommitHashes.size).toBe(0);
        expect(s.state.checkpointHashesVersion).toBe(0);
    });

    it('state is deeply reactive via $state()', () => {
        const s = new StreamState();
        expect(s.state.entries).toEqual([]);

        s.ensureAssistantEntry(() => 'reactive-test');
        expect(s.state.entries.length).toBe(1);

        // Mutating entry content should be reflected in state
        s.state.entries[0].content = 'Hello';
        expect(s.state.entries[0].content).toBe('Hello');

        // Further mutations also work
        s.state.entries[0].content = 'Hello World';
        expect(s.state.entries[0].content).toBe('Hello World');
    });

    it('multiple ensureAssistantEntry calls create separate entries', () => {
        const s = new StreamState();
        s.ensureAssistantEntry(() => 'id-1');
        s.closeCurrentAssistant();
        s.ensureAssistantEntry(() => 'id-2');
        s.closeCurrentAssistant();
        s.ensureAssistantEntry(() => 'id-3');

        expect(s.state.entries.length).toBe(3);
        expect(s.state.entries.map(e => e.id)).toEqual(['id-1', 'id-2', 'id-3']);
        expect(s.state.entries[0].isStreaming).toBe(false);
        expect(s.state.entries[1].isStreaming).toBe(false);
        expect(s.state.entries[2].isStreaming).toBe(true); // current one still streaming
    });

    it('resetStreaming preserves sessionCost', () => {
        const s = new StreamState();
        s.state.sessionCost = 0.42;
        s.ensureAssistantEntry(() => 'id-1');
        s.resetStreaming();
        expect(s.state.sessionCost).toBe(0.42);
        expect(s.state.streamTokenCount).toBe(0);
        expect(s.state.awaitingResponse).toBe(false);
    });

    it('full reset clears sessionCost', () => {
        const s = new StreamState();
        s.state.sessionCost = 1.5;
        s.ensureAssistantEntry(() => 'id-1');
        s.reset();
        expect(s.state.sessionCost).toBe(0);
        expect(s.state.entries).toEqual([]);
    });
});

describe('buildLog reactivity (proxy)', () => {
    it('mutating the proxied entry returned after creation notifies effects', async () => {
        const s = new StreamState();
        let observedLen = -1;
        const unsub = $effect.root(() => {
            $effect(() => {
                for (const e of s.state.entries) {
                    if (e.buildLog) observedLen = e.buildLog.lines.length;
                }
            });
        });

        // Simulate ensureBuildLogEntry: assign raw entry, then re-read through the proxy.
        const raw: ChatEntry = { id: 'b1', role: 'system', content: 'Building...', isStreaming: false, createdAt: new Date().toISOString(), buildLog: { title: 'Building...', lines: [] } };
        s.state.entries = [...s.state.entries, raw];
        const proxied = s.state.entries[s.state.entries.length - 1]!;
        proxied.buildLog!.lines.push('Step 1/5');
        await Promise.resolve();
        expect(observedLen).toBe(1);
        proxied.buildLog!.lines.push('Step 2/5');
        await Promise.resolve();
        expect(observedLen).toBe(2);
        unsub();
    });
});

describe('lockWait lifetime', () => {
    it('reset() clears an announced wait', () => {
        const s = new StreamState();
        s.state.lockWait = { domain: 'worktree', seconds: 30 };
        s.reset();
        expect(s.state.lockWait).toBeNull();
    });

    it('clearAwaiting() clears it too — the turn is over, so nothing is queued', () => {
        const s = new StreamState();
        s.state.lockWait = { domain: 'stash', seconds: 6 };
        s.clearAwaiting();
        expect(s.state.lockWait).toBeNull();
    });
});

function toolEntry(id: string, calls: Array<{ toolId: string; isPending: boolean; result?: string }>): ChatEntry {
    return {
        id, role: 'assistant', content: '', isStreaming: false, createdAt: new Date().toISOString(),
        toolCalls: calls.map(c => ({ toolId: c.toolId, toolName: 'bash', args: {}, isPending: c.isPending, result: c.result })),
    };
}

describe('dropUnresolvedToolCalls', () => {
    it('drops the card the server never answered, and the entry that carries it alone', () => {
        const s = new StreamState();
        s.state.entries = [
            { id: 'u1', role: 'user', content: 'do it', isStreaming: false, createdAt: 'now' },
            toolEntry('tc-entry', [{ toolId: 'call-1', isPending: true }]),
        ];
        s.state.activeToolCalls = new Map([['call-1', { entryId: 'tc-entry', toolCall: s.state.entries[1].toolCalls![0]! }]]);

        s.dropUnresolvedToolCalls();

        expect(s.state.entries.map(e => e.id)).toEqual(['u1']);
        expect(s.state.activeToolCalls.size).toBe(0);
    });

    it('keeps every answered call, however the entry mixes them', () => {
        const s = new StreamState();
        s.state.entries = [
            toolEntry('done', [{ toolId: 'call-1', isPending: false, result: 'ok' }]),
            toolEntry('mixed', [
                { toolId: 'call-2', isPending: false, result: 'ok' },
                { toolId: 'call-3', isPending: true },
            ]),
        ];

        s.dropUnresolvedToolCalls();

        // The entry goes whole: the turn that abandoned the call persisted neither its text row nor the tool row.
        expect(s.state.entries.map(e => e.id)).toEqual(['done']);
    });

    it('is inert with nothing pending — the entry list keeps its identity', () => {
        const s = new StreamState();
        s.state.entries = [toolEntry('done', [{ toolId: 'call-1', isPending: false, result: 'ok' }])];
        const before = s.state.entries;

        s.dropUnresolvedToolCalls();

        expect(s.state.entries).toBe(before);
    });

    it('leaves a resolved call\'s ref alone — only the vanished call\'s ref is dropped', () => {
        const s = new StreamState();
        s.state.entries = [
            toolEntry('kept', [{ toolId: 'call-1', isPending: false, result: 'ok' }]),
            toolEntry('gone', [{ toolId: 'call-2', isPending: true }]),
        ];
        s.state.activeToolCalls = new Map([
            ['call-1', { entryId: 'kept', toolCall: s.state.entries[0].toolCalls![0]! }],
            ['call-2', { entryId: 'gone', toolCall: s.state.entries[1].toolCalls![0]! }],
        ]);

        s.dropUnresolvedToolCalls();

        expect([...s.state.activeToolCalls.keys()]).toEqual(['call-1']);
    });
});
