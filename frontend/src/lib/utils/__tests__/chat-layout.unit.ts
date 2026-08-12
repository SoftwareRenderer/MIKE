import { describe, it, expect } from 'vitest';
import { computeInterleavedLayout, TURN_ENDING_TOOLS, type LayoutToolCall } from '$lib/utils/chat-layout';

function tool(toolId: string, toolName: string, reasoningIndex: number, contentIndex: number): LayoutToolCall {
    return { toolId, toolName, reasoningIndex, contentIndex };
}

describe('computeInterleavedLayout', () => {
    it('returns null with no tool calls', () => {
        expect(computeInterleavedLayout(undefined, 'reasoning', 'content')).toBeNull();
        expect(computeInterleavedLayout([], 'reasoning', 'content')).toBeNull();
    });

    it('returns null when tools have no position data', () => {
        const tcs: LayoutToolCall[] = [{ toolId: 't1', toolName: 'read_file' }];
        expect(computeInterleavedLayout(tcs, 'reasoning', 'content')).toBeNull();
    });

    it('renders ask_question after content (null → standardContent) when it is the only positioned tool', () => {
        // Reasoning completes (length 13), then content streams (length 20), then ask_question is called.
        // reasoningIndex === reasoning length would previously place the card inside reasoning, before content.
        const reasoning = 'Let me think';
        const content = 'Here is my question:';
        const tcs = [tool('ask-1', 'ask_question', reasoning.length, content.length)];
        expect(computeInterleavedLayout(tcs, reasoning, content)).toBeNull();
    });

    it('renders request_review after content (null → standardContent) when it is the only positioned tool', () => {
        const reasoning = 'Reviewing changes';
        const content = 'All done here.';
        const tcs = [tool('rr-1', 'request_review', reasoning.length, content.length)];
        expect(computeInterleavedLayout(tcs, reasoning, content)).toBeNull();
    });

    it('keeps non turn-ending tools interleaved within reasoning (existing behavior)', () => {
        const reasoning = 'I need to read the file';
        const tcs = [tool('r-1', 'read_file', reasoning.length, 0)];
        const layout = computeInterleavedLayout(tcs, reasoning, '');
        expect(layout).not.toBeNull();
        expect(layout!.reasoningSegments).toEqual([
            { type: 'reasoning', text: reasoning },
            { type: 'tool', tc: tcs[0] },
        ]);
        expect(layout!.trailingTools).toEqual([]);
    });

    it('routes a trailing ask_question to trailingTools when other tools interleave within reasoning', () => {
        const reasoning = 'First inspect the file, then ask';
        const content = 'Here is my question:';
        const read = tool('r-1', 'read_file', 5, 0);
        const ask = tool('ask-1', 'ask_question', reasoning.length, content.length);
        const layout = computeInterleavedLayout([read, ask], reasoning, content);
        expect(layout).not.toBeNull();
        // The read_file tool remains interleaved inside reasoning...
        const toolsInReasoning = layout!.reasoningSegments.filter(s => s.type === 'tool').map(s => s.tc);
        expect(toolsInReasoning).toEqual([read]);
        // ...while ask_question trails the content.
        expect(layout!.trailingTools).toEqual([ask]);
        expect(layout!.trailingContent).toBe(content);
    });

    it('declares ask_question and request_review as the turn-ending tools', () => {
        expect(TURN_ENDING_TOOLS.has('ask_question')).toBe(true);
        expect(TURN_ENDING_TOOLS.has('request_review')).toBe(true);
        expect(TURN_ENDING_TOOLS.has('read_file')).toBe(false);
    });
});
