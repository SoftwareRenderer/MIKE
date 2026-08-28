import type { ChatEntry } from '$lib/types';
import type { ToolCallInfo } from '$lib/services/stream-state.svelte';

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
 * Resolve the live tool call object for a tool_id.
 *
 * `activeToolCalls` is only a fast-path hint, never the source of truth: a
 * history reload (loadHistory) replaces buffer entries with DB-loaded ones that
 * keep the same tool_id but carry a different entry id, which silently
 * invalidates a cached `{entryId, toolCall}` ref. A ref is honoured only while
 * it still points into `entries`; otherwise entries are scanned by tool_id.
 *
 * Without this, a `tool_result` whose ref went stale resolves to nothing, its
 * `isPending` is never cleared and the card keeps spinning "Running" although
 * the backend finished the tool long ago.
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
