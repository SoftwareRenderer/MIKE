/**
 * Interleaved chat-message layout: decides where tool calls render relative to
 * the assistant's reasoning and content. Extracted from ChatMessage.svelte so it
 * can be unit-tested directly.
 */

/** Minimal tool-call shape the layout computation needs. */
export interface LayoutToolCall {
    toolId: string;
    toolName: string;
    contentIndex?: number;
    reasoningIndex?: number;
}

/** A segment of the reasoning region — either reasoning text or an interleaved tool. */
export type ChatSegment<T extends LayoutToolCall = LayoutToolCall> =
    | { type: 'reasoning'; text: string }
    | { type: 'tool'; tc: T };

/** Computed layout: interleaved reasoning segments, trailing content, trailing tools. */
export interface InterleavedLayout<T extends LayoutToolCall = LayoutToolCall> {
    reasoningSegments: ChatSegment<T>[];
    trailingContent: string;
    trailingTools: T[];
}

/**
 * Turn-ending tools that carry user-facing content (a question or a review
 * summary). They are always displayed AFTER the assistant's content message,
 * never interleaved inside the reasoning region.
 */
export const TURN_ENDING_TOOLS: ReadonlySet<string> = new Set(['ask_question', 'request_review']);

/**
 * Build the interleaved layout for an assistant message whose tool calls carry
 * contentIndex + reasoningIndex, captured when the event streamed in. A tool call whose
 * reasoningIndex falls inside the reasoning text renders there; the rest trail the content
 * bubble, and turn-ending tools (ask_question / request_review) always trail it so the card
 * sits below the message text. Returns null when there is nothing to interleave.
 */
export function computeInterleavedLayout<T extends LayoutToolCall>(
    tcs: T[] | undefined,
    reasoning: string | undefined,
    txt: string | undefined,
): InterleavedLayout<T> | null {
    if (!tcs || tcs.length === 0) return null;
    const hasPositionData = tcs.some(tc => tc.contentIndex !== undefined && tc.reasoningIndex !== undefined);
    if (!hasPositionData) return null;

    const reasoningText = reasoning || '';
    const contentText = txt || '';
    const positioned = tcs.filter(tc => tc.contentIndex !== undefined && tc.reasoningIndex !== undefined);
    const unpositioned = tcs.filter(tc => tc.contentIndex === undefined || tc.reasoningIndex === undefined);

    // Turn-ending tools always trail the content, never interleave with reasoning.
    const turnEnding = positioned.filter(tc => TURN_ENDING_TOOLS.has(tc.toolName));
    const regular = positioned.filter(tc => !TURN_ENDING_TOOLS.has(tc.toolName));

    const withinReasoning = regular.filter(tc => reasoningText.length > 0 && (tc.reasoningIndex || 0) <= reasoningText.length).sort((a, b) => (a.reasoningIndex || 0) - (b.reasoningIndex || 0));
    const afterReasoning = regular.filter(tc => reasoningText.length === 0 || (tc.reasoningIndex || 0) > reasoningText.length).sort((a, b) => (a.contentIndex || 0) - (b.contentIndex || 0));

    const reasoningSegments: ChatSegment<T>[] = [];
    let lastIdx = 0;
    for (const tc of withinReasoning) {
        const rIdx = tc.reasoningIndex || 0;
        if (rIdx > lastIdx) {
            reasoningSegments.push({ type: 'reasoning', text: reasoningText.slice(lastIdx, rIdx) });
        }
        reasoningSegments.push({ type: 'tool', tc });
        lastIdx = rIdx;
    }
    if (lastIdx < reasoningText.length) {
        reasoningSegments.push({ type: 'reasoning', text: reasoningText.slice(lastIdx) });
    }
    if (withinReasoning.length === 0) return null;

    return { reasoningSegments, trailingContent: contentText, trailingTools: afterReasoning.concat(unpositioned, turnEnding) };
}
