import { describe, it, expect, vi } from 'vitest';
import { StreamState } from '$lib/services/stream-state.svelte';
import { createStreamHandler, type StreamHandlerCallbacks } from '$lib/services/stream-handler';
import { buildEntriesFromMessages, entriesAwaitingRows, mergeLoadedHistory } from '$lib/utils/chat-entries';
import type { ChatEntry } from '$lib/types';

/** Both sides of the merge come from the real builders, so a change to either is felt here. */

function makeCallbacks(): StreamHandlerCallbacks {
    let n = 0;
    return {
        genId: () => `live-${n++}`,
        scroll: vi.fn(),
        markStreaming: vi.fn(),
        markNotStreaming: vi.fn(),
        onRefreshDiff: vi.fn(),
        onThreadSwitch: vi.fn(),
        loadHistory: vi.fn(),
        updateActiveThread: vi.fn(),
        updateThreadUrl: vi.fn(),
        onNewError: vi.fn(),
    };
}

/** Drive the real stream handler to get what the browser holds before a history load. */
function liveEntries(events: Array<{ type: string; data: unknown }>): ChatEntry[] {
    const stream = new StreamState();
    const handle = createStreamHandler(stream, makeCallbacks());
    for (const e of events) handle({ type: e.type as never, data: e.data } as never, false);
    return stream.state.entries;
}

const toolRow = (id: string, toolId: string, toolName: string, result?: string) => ({
    id,
    role: 'tool_call',
    content: '',
    thread_id: 'th1',
    created_at: '2026-09-10T10:00:00Z',
    tool_calls: JSON.stringify({ tool_id: toolId, tool_name: toolName, args: { path: 'a.ts' } }),
    ...(result ? { tool_result: JSON.stringify({ output: result, success: true, duration: 5 }) } : {}),
});

const assistantRow = (id: string, over: Record<string, unknown> = {}) => ({
    id,
    role: 'assistant',
    content: '',
    thread_id: 'th1',
    created_at: '2026-09-10T10:00:00Z',
    ...over,
});

const userRow = (id: string, content: string) => ({ id, role: 'user', content, thread_id: 'th1', created_at: '2026-09-10T09:59:00Z' });
const errorRow = (id: string, content: string) => ({ id, role: 'error', content, thread_id: 'th1', created_at: '2026-09-10T10:06:00Z' });

function toolIdsOf(entries: ChatEntry[]): string[] {
    return entries.flatMap(e => (e.toolCalls ?? []).map(tc => tc.toolId));
}

/** How many entries would draw a thinking block. Two means the same turn is on screen twice. */
function reasoningEntryCount(entries: ChatEntry[]): number {
    return entries.filter(e => e.reasoningContent || e.hasReasoning).length;
}

describe('mergeLoadedHistory — a turn the DB has only partly persisted', () => {
    it('keeps the tool calls of a multi-call turn that has no row yet while the first one runs', () => {
        const messages = [
            userRow('u1', 'do the thing'),
            assistantRow('a1', { reasoning_content: 'I will read, then edit, then test.' }),
            toolRow('m1', 't1', 'read_file'),
        ];
        const live = liveEntries([
            { type: 'reasoning', data: { text: 'I will read, then edit, then test.' } },
            { type: 'tool_call', data: { tool_id: 't1', tool_name: 'read_file', args: {} } },
            { type: 'tool_call', data: { tool_id: 't2', tool_name: 'edit_file', args: {} } },
            { type: 'tool_call', data: { tool_id: 't3', tool_name: 'bash', args: {} } },
        ]);

        const merged = mergeLoadedHistory(buildEntriesFromMessages(messages).entries, messages.map(m => m.id), live, true);

        expect(toolIdsOf(merged).sort()).toEqual(['t1', 't2', 't3']);
        // The cards join the DB's copy of the turn, so it keeps the row id — what the checkpoint
        // divider keys its "view changes" link on.
        const turn = merged.find(e => e.toolCalls?.some(tc => tc.toolId === 't2'));
        expect(turn?.id).toBe('a1');
        expect(turn?.toolCalls?.map(tc => tc.toolId)).toEqual(['t1', 't2', 't3']);
        expect(reasoningEntryCount(merged)).toBe(1);
        expect(merged).toHaveLength(2);
    });

    it('drops a live entry whose tool calls are all in the DB, so no card is drawn twice', () => {
        const messages = [
            userRow('u1', 'go'),
            assistantRow('a1', { reasoning_content: 'thinking' }),
            toolRow('m1', 't1', 'read_file', 'file body'),
            toolRow('m2', 't2', 'bash', 'ok'),
        ];
        const live = liveEntries([
            { type: 'reasoning', data: { text: 'thinking' } },
            { type: 'tool_call', data: { tool_id: 't1', tool_name: 'read_file', args: {} } },
            { type: 'tool_call', data: { tool_id: 't2', tool_name: 'bash', args: {} } },
        ]);

        const merged = mergeLoadedHistory(buildEntriesFromMessages(messages).entries, messages.map(m => m.id), live, true);

        expect(toolIdsOf(merged)).toEqual(['t1', 't2']);
        expect(reasoningEntryCount(merged)).toBe(1);
    });

    it('keeps the thinking of a turn that is still streaming, even though earlier turns had tool calls', () => {
        const messages = [
            userRow('u1', 'go'),
            assistantRow('a1', { content: 'done earlier' }),
            toolRow('m1', 't1', 'read_file', 'file body'),
        ];
        const live = liveEntries([
            { type: 'reasoning', data: { text: 'Now let me consider what to change next…' } },
        ]);

        const merged = mergeLoadedHistory(buildEntriesFromMessages(messages).entries, messages.map(m => m.id), live, true);

        expect(merged.some(e => e.reasoningContent === 'Now let me consider what to change next…')).toBe(true);
    });

    it('drops a live entry whose finished text is what the DB already carries', () => {
        // The shape an oversized tool_call frame leaves behind: it was never buffered, so the
        // browser holds the turn's text and no card while the DB has both.
        const messages = [
            userRow('u1', 'go'),
            assistantRow('a1', { content: 'The answer is 42.', reasoning_content: '[HIDDEN]', has_reasoning: true }),
            toolRow('m1', 't1', 'read_file', 'file body'),
        ];
        const live = liveEntries([
            { type: 'reasoning', data: { text: 'Counting…' } },
            { type: 'token', data: { text: 'The answer is 42.' } },
            { type: 'tool_result', data: { tool_id: 't1', success: true, output: 'file body', duration: 4 } },
        ]);

        const merged = mergeLoadedHistory(buildEntriesFromMessages(messages).entries, messages.map(m => m.id), live, true);

        expect(merged.filter(e => e.content === 'The answer is 42.')).toHaveLength(1);
        expect(reasoningEntryCount(merged)).toBe(1);
    });

    it('keeps a streaming entry even when the text so far matches the newest row', () => {
        // Two turns can open with the same words, and only the persisted one is closed.
        const messages = [
            userRow('u1', 'go'),
            assistantRow('a1', { content: 'The answer is 42.' }),
            toolRow('m1', 't1', 'read_file', 'file body'),
        ];
        const live = liveEntries([
            { type: 'token', data: { text: 'The answer is 42.' } },
            { type: 'reasoning', data: { text: ' — and let me now explain why.' } },
        ]);

        const merged = mergeLoadedHistory(buildEntriesFromMessages(messages).entries, messages.map(m => m.id), live, true);

        // The live turn is the one still thinking, so the match proves nothing and it stays.
        expect(merged.some(e => e.reasoningContent === ' — and let me now explain why.')).toBe(true);
    });
});

describe('mergeLoadedHistory — a live copy of a row the read brought', () => {
    it('retires the copies a thawed tab leaves below the rows whose text they repeat', () => {
        // The tab was frozen mid-turn, so these tokens landed after the read's rows. The newest row is an
        // error, so a rule that reads only the newest row has nothing to compare against here.
        const messages = [
            userRow('u1', 'do it'),
            assistantRow('a1', { content: 'The command completed.' }),
            userRow('u2', 'Is the task complete?'),
            assistantRow('a2', { content: 'The command completed.' }),
            errorRow('e1', '[loop_detected] Model stuck in loop'),
        ];
        const live = liveEntries([
            { type: 'token', data: { text: 'The command ' } },
            { type: 'token', data: { text: 'completed.' } },
            { type: 'hint', data: { id: 'u2', role: 'user', content: 'Is the task complete?' } },
            { type: 'token', data: { text: 'The command completed.' } },
            { type: 'user_message', data: { id: 'e1', role: 'error', content: 'Model stuck in loop' } },
        ]);

        const merged = mergeLoadedHistory(buildEntriesFromMessages(messages).entries, messages.map(m => m.id), live, false);

        expect(merged.map(e => e.id)).toEqual(['u1', 'a1', 'u2', 'a2', 'e1']);
    });

    it('keeps the streaming answer that repeats an older row, while it is the newest thing on screen', () => {
        // Two calls in one conversation do answer with the same words.
        const messages = [
            userRow('u1', 'go'),
            assistantRow('a1', { content: 'The command completed.' }),
            userRow('u2', 'Is the task complete?'),
        ];
        const live = liveEntries([
            { type: 'hint', data: { id: 'u2', role: 'user', content: 'Is the task complete?' } },
            { type: 'token', data: { text: 'The command completed.' } },
        ]);

        const merged = mergeLoadedHistory(buildEntriesFromMessages(messages).entries, messages.map(m => m.id), live, true);

        expect(merged.filter(e => e.content === 'The command completed.')).toHaveLength(2);
        expect(merged[merged.length - 1].isStreaming).toBe(true);
    });

    it('keeps a live answer none of the rows holds, closed or not', () => {
        const messages = [userRow('u1', 'go'), assistantRow('a1', { content: 'The command completed.' })];
        const live = liveEntries([
            { type: 'token', data: { text: 'Now the tests are green; the fix is in place.' } },
            { type: 'user_message', data: { id: 'u2', role: 'user', content: 'thanks' } }, // closes it
        ]);

        const merged = mergeLoadedHistory(buildEntriesFromMessages(messages).entries, messages.map(m => m.id), live, true);

        expect(merged.some(e => e.content === 'Now the tests are green; the fix is in place.')).toBe(true);
        expect(merged.map(e => e.id)).toEqual(['u1', 'a1', 'live-0', 'u2']);
    });

    it('retires a streaming copy once something newer is on screen below it', () => {
        const messages = [userRow('u1', 'go'), assistantRow('a1', { content: 'The command completed.' })];
        const live: ChatEntry[] = [
            { id: 'live-0', role: 'assistant', content: 'The command compl', isStreaming: true },
            { id: 'live-1', role: 'advisory', content: '2 diagnostics', kind: 'diagnostics', isStreaming: false },
        ];

        const merged = mergeLoadedHistory(buildEntriesFromMessages(messages).entries, messages.map(m => m.id), live, true);

        expect(merged.map(e => e.id)).toEqual(['u1', 'a1', 'live-1']);
    });

    it('keeps a live entry that holds a call the rows do not have, whatever its text repeats', () => {
        const messages = [
            userRow('u1', 'go'),
            assistantRow('a1', { content: 'Reading now.' }),
            toolRow('m1', 't1', 'read_file', 'file body'),
            errorRow('e1', '[loop_detected] Model stuck in loop'),
        ];
        const live = liveEntries([
            { type: 'token', data: { text: 'Reading now.' } },
            { type: 'tool_result', data: { tool_id: 't1', success: true, output: 'file body', duration: 3 } },
            { type: 'tool_call', data: { tool_id: 't2', tool_name: 'bash', args: {} } },
        ]);

        const merged = mergeLoadedHistory(buildEntriesFromMessages(messages).entries, messages.map(m => m.id), live, true);

        expect(toolIdsOf(merged).sort()).toEqual(['t1', 't2']);
        expect(merged.filter(e => e.content === 'Reading now.')).toHaveLength(1);
    });
});

describe('mergeLoadedHistory — a task that is not running', () => {
    it('hides a tool call whose result never arrived, without hiding the message around it', () => {
        const messages = [
            userRow('u1', 'go'),
            assistantRow('a1', { reasoning_content: 'almost finished', content: 'Wrapping up.' }),
            toolRow('m1', 't1', 'read_file', 'file body'),
            toolRow('m2', 't2', 'bash'), // the server died before the result was written
        ];

        const merged = mergeLoadedHistory(buildEntriesFromMessages(messages).entries, messages.map(m => m.id), [], false);

        // Three rows, two entries: the tool calls merge into the assistant entry.
        expect(merged).toHaveLength(2);
        expect(toolIdsOf(merged)).toEqual(['t1']);
        const turn = merged.find(e => e.toolCalls?.length);
        expect(turn?.content).toBe('Wrapping up.');
        expect(turn?.reasoningContent).toBe('almost finished');
    });

    it('drops a message whose only content was the tool call that never finished', () => {
        const messages = [
            userRow('u1', 'go'),
            assistantRow('a1'),
            toolRow('m1', 't1', 'read_file'),
        ];

        const merged = mergeLoadedHistory(buildEntriesFromMessages(messages).entries, messages.map(m => m.id), [], false);

        expect(toolIdsOf(merged)).toEqual([]);
    });

    it('keeps the in-flight tool call card of a running task', () => {
        const messages = [userRow('u1', 'go'), assistantRow('a1'), toolRow('m1', 't1', 'read_file')];

        const merged = mergeLoadedHistory(buildEntriesFromMessages(messages).entries, messages.map(m => m.id), [], true);

        expect(toolIdsOf(merged)).toEqual(['t1']);
    });
});

describe('mergeLoadedHistory — identity', () => {
    it('keeps the DB entry when the same id arrived twice (complete landed after the load)', () => {
        const messages = [userRow('u1', 'go'), assistantRow('a1', { content: 'from the DB' })];
        const dbEntries = buildEntriesFromMessages(messages).entries;
        const live: ChatEntry[] = [{ id: 'a1', role: 'assistant', content: 'streamed copy', isStreaming: false }];

        const merged = mergeLoadedHistory(dbEntries, messages.map(m => m.id), live, true);

        expect(merged.filter(e => e.id === 'a1')).toHaveLength(1);
        expect(merged.find(e => e.id === 'a1')?.content).toBe('from the DB');
    });

    it('appends a live entry the DB does not describe at all, after the DB entries', () => {
        const messages = [userRow('u1', 'go'), assistantRow('a1', { content: 'earlier' })];
        const live: ChatEntry[] = [{ id: 'live-0', role: 'advisory', content: '2 diagnostics', kind: 'diagnostics' }];

        const merged = mergeLoadedHistory(buildEntriesFromMessages(messages).entries, messages.map(m => m.id), live, false);

        expect(merged.map(e => e.id)).toEqual(['u1', 'a1', 'live-0']);
    });
});

describe('entriesAwaitingRows — what a replaced conversation leaves on screen', () => {
    const settled = (id: string, content: string): ChatEntry => ({
        id, role: 'assistant', content, isStreaming: false,
    });

    it('keeps nothing when every answer on screen has had its turn at the database', () => {
        const onScreen = [settled('u1', 'go'), settled('a1', 'an answer'), settled('a2', 'another')];

        expect(entriesAwaitingRows(onScreen).map(e => e.id)).toEqual([]);
    });

    it('keeps the answer streaming now, and everything written after it', () => {
        const onScreen = [
            settled('a1', 'an answer'),
            { id: 'live-0', role: 'assistant', content: 'Working on', isStreaming: true } as ChatEntry,
        ];

        expect(entriesAwaitingRows(onScreen).map(e => e.id)).toEqual(['live-0']);
    });

    it('keeps the turn whose tool call is still unanswered, text and card together', () => {
        const onScreen = [
            settled('a1', 'an answer'),
            { ...settled('live-1', 'Let me check'), toolCalls: [{ toolId: 'call-1', toolName: 'bash', args: {}, isPending: true } as never] },
        ];

        // The card shares the entry of the text that announced it.
        expect(entriesAwaitingRows(onScreen).map(e => e.id)).toEqual(['live-1']);
    });

    it('keeps a send the server has not accepted', () => {
        const onScreen = [settled('a1', 'an answer'), { id: 'pend-1', role: 'user', content: 'next', isStreaming: false, queued: true } as ChatEntry];

        expect(entriesAwaitingRows(onScreen).map(e => e.id)).toEqual(['pend-1']);
    });

    it('keeps a sandbox build box that is still writing', () => {
        const onScreen = [
            settled('a1', 'an answer'),
            { id: 'live-2', role: 'system', content: 'Building', isStreaming: false, buildLog: { title: 'Building', lines: ['step 1'] } } as ChatEntry,
        ];

        expect(entriesAwaitingRows(onScreen).map(e => e.id)).toEqual(['live-2']);
    });

    it('drops a finished build box, which is a row like any other', () => {
        const onScreen = [
            { id: 'live-2', role: 'system', content: 'Building', isStreaming: false, buildLog: { title: 'Building', lines: ['step 1'], done: true } } as ChatEntry,
        ];

        expect(entriesAwaitingRows(onScreen)).toEqual([]);
    });

    it('reads the entries the stream left, not a hand-written list', () => {
        const live = liveEntries([
            { type: 'token', data: { text: 'Working on it.' } },
            { type: 'tool_call', data: { tool_id: 'call-1', tool_name: 'bash', args: { command: 'sleep 5' } } },
            { type: 'tool_result', data: { tool_id: 'call-1', output: 'done', success: true } },
            { type: 'token', data: { text: 'The command' } },
        ]);
        expect(live.length).toBeGreaterThan(1);

        // The call answered, so only the text streaming after it is still awaiting a row.
        expect(entriesAwaitingRows(live).every(e => e.isStreaming)).toBe(true);
    });
});
