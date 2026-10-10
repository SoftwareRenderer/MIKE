import { describe, it, expect, vi } from 'vitest';
import { StreamState } from '$lib/services/stream-state.svelte';
import { createStreamHandler, type StreamHandlerCallbacks } from '$lib/services/stream-handler';
import { applyWindowRead } from '$lib/utils/chat-window';
import { buildEntriesFromMessages, entriesWithoutRows, mergeLoadedHistory } from '$lib/utils/chat-entries';
import type { ChatEntry } from '$lib/types';

/**
 * A newest-window read that shares no row with the rows on screen replaces the conversation, and the browser's
 * own copy of the run is merged on top of it. A merge can only append, so anything kept there draws below the
 * newest answer and the turns end up out of the order they were written.
 *
 * The fixture is a long run of two-row answers, so five answers is ten rows and a tab that was away for a
 * while reads a window with nothing in common with the rows it holds.
 */

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

/** Drive the real stream handler, so the tail is what the frames actually leave behind. */
function liveEntries(events: Array<{ type: string; data: unknown }>, idPrefix = 'live-'): ChatEntry[] {
    const stream = new StreamState();
    let n = 0;
    const handle = createStreamHandler(stream, { ...makeCallbacks(), genId: () => `${idPrefix}${n++}` });
    for (const e of events) handle({ type: e.type as never, data: e.data } as never, false);
    return stream.state.entries;
}

const at = (answer: number) => `2026-10-10T11:00:${String(answer).padStart(2, '0')}.000000000Z`;

/** One answer: the assistant row and the `tool_call` row under it, as the writer orders them. */
function answerRowPair(n: number) {
    return [
        { id: `a${n}`, role: 'assistant', content: `answer ${n}`, thread_id: 'th1', created_at: at(n) },
        {
            id: `m${n}`, role: 'tool_call', content: '', thread_id: 'th1', created_at: at(n),
            tool_calls: JSON.stringify({ tool_id: `call-${n}`, tool_name: 'bash', args: { command: 'ls' } }),
            tool_result: JSON.stringify({ output: 'ok', success: true, duration: 4 }),
        },
    ];
}

/** The read's window: none of it overlaps the rows on screen. */
const readRows = [7, 8, 9, 10, 11, 12, 13].flatMap(answerRowPair);
/** The rows on screen before the read. */
const heldRows = [1, 2, 3, 4, 5, 6].flatMap(answerRowPair);

/** Entry ids in draw order, oldest first. */
function drawnIds(entries: ChatEntry[]): string[] {
    return entries.map(e => e.id);
}

const readEntryIds = () => drawnIds(buildEntriesFromMessages(readRows).entries);

/** The transcript after a read that could not be joined. Mirrors `loadHistory` → `renderHistory`
 * (`TaskChat.svelte`), the rule's only caller. */
function redraw(held: typeof heldRows, read: typeof readRows, drawn: ChatEntry[], running: boolean): ChatEntry[] {
    const joined = applyWindowRead(held, read);
    expect(joined.keepsEarlier).toBe(false); // every case here needs a read that cannot be joined
    const trimmed = entriesWithoutRows(drawn);
    const { entries } = buildEntriesFromMessages(joined.rows);
    return mergeLoadedHistory(entries, joined.rows.map(r => r.id), trimmed, running);
}

/** The turn the socket dropped on: its card was announced and never answered. Behind it, a note and a prompt
 * the hub replayed, whose rows were written before the read's window opens. */
function tailWithStuckCard(): ChatEntry[] {
    return liveEntries([
        { type: 'token', data: { text: 'checking the caps' } },
        { type: 'tool_call', data: { tool_id: 'call-6', tool_name: 'bash', args: { command: 'capsh' } } },
        { type: 'advisory', data: { id: 'adv-5', kind: 'diagnostics', text: '2 diagnostics', created_at: at(5) } },
        { type: 'user_message', data: { id: 'u6', role: 'user', content: 'try again', created_at: at(6) } },
    ]);
}

describe('entriesWithoutRows — a read that replaces the conversation', () => {
    it('does not drag the turns behind an unanswered card below the answers the read brought', () => {
        const drawn = tailWithStuckCard();
        expect(drawnIds(drawn)).toEqual(['live-0', 'adv-5', 'u6']);

        const merged = redraw(heldRows, readRows, drawn, true);

        expect(drawnIds(merged)).toEqual(readEntryIds());
    });

    it('retires the same tail for a task that is not running', () => {
        const merged = redraw(heldRows, readRows, tailWithStuckCard(), false);

        expect(drawnIds(merged)).toEqual(readEntryIds());
    });

    it('keeps the answer streaming now, and nothing of the turns above it', () => {
        const streaming = liveEntries([{ type: 'token', data: { text: 'the turn in flight' } }], 'now-');
        const drawn = [...tailWithStuckCard(), ...streaming];

        const merged = redraw(heldRows, readRows, drawn, true);

        expect(drawnIds(merged)).toEqual([...readEntryIds(), 'now-0']);
        expect(merged[merged.length - 1]!.isStreaming).toBe(true);
    });

    it('keeps a send the server has not accepted, though its words repeat an older prompt', () => {
        const drawn = [...tailWithStuckCard(), { id: 'pend-1', role: 'user', content: 'try again', isStreaming: false, queued: true } as ChatEntry];

        const merged = redraw(heldRows, readRows, drawn, true);

        expect(drawnIds(merged).slice(-1)).toEqual(['pend-1']);
    });

    it('keeps a notice this browser wrote, which no row will ever hold', () => {
        const drawn = [
            ...liveEntries([
                { type: 'token', data: { text: 'answer 6' } },
                { type: 'complete', data: { state: 'done', assistant_message_id: 'a6' } },
            ]),
            { id: 'send-failed:x', role: 'error', content: 'Send failed', isStreaming: false, local: true } as ChatEntry,
        ];

        const merged = redraw(heldRows, readRows, drawn, true);

        expect(drawnIds(merged).slice(-1)).toEqual(['send-failed:x']);
    });
});
