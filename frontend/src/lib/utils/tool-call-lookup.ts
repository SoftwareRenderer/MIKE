import type { ChatEntry, ToolCallInfo } from '$lib/types';

/** A live tool call and the entry that currently holds it. */
export interface LiveToolCall {
    entry: ChatEntry;
    tc: ToolCallInfo;
}

/** What `StreamState.state.activeToolCalls` maps a tool_id to. */
export type ToolCallRef = { entryId: string; toolCall: ToolCallInfo };

/**
 * Find the tool call with this id in the CURRENT entry list.
 *
 * The returned object is reached through `entries`, i.e. it is the reactive
 * (proxied) instance: mutating it notifies subscribers, mutating a raw copy
 * captured earlier does not. Callers that stream data into a tool call must
 * therefore always go through this lookup rather than keep an object around.
 */
export function findToolCall(entries: ChatEntry[], toolId: string): LiveToolCall | undefined {
    if (!toolId) return undefined;
    for (const entry of entries) {
        const calls = entry.toolCalls;
        if (!calls?.length) continue;
        const tc = calls.find((t: ToolCallInfo) => t.toolId === toolId);
        if (tc) return { entry, tc };
    }
    return undefined;
}

/**
 * Resolve the live tool call object for a tool_id. `activeToolCalls` is a fast-path hint,
 * never the source of truth: a history reload replaces buffer entries with DB rows that keep
 * the tool_id but change the entry id, which invalidates a cached ref. A ref is honoured only
 * while it still points into `entries`; otherwise entries are scanned by tool_id. Without
 * this a stale ref leaves a finished tool card spinning on "Running".
 */
export function resolveLiveToolCall(
    entries: ChatEntry[],
    activeToolCalls: Map<string, ToolCallRef>,
    toolId: string,
): LiveToolCall | undefined {
    const ref = activeToolCalls.get(toolId);
    if (ref) {
        const entry = entries.find(e => e.id === ref.entryId);
        const tc = entry?.toolCalls?.find((t: ToolCallInfo) => t.toolId === toolId);
        if (entry && tc) return { entry, tc };
    }
    return findToolCall(entries, toolId);
}
