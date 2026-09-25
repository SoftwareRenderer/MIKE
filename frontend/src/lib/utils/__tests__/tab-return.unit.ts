import { describe, it, expect } from 'vitest';
import {
    needsReturnReconcile,
    WAKE_FRAME_GAP_MS,
    RETURN_DEDUPE_MS,
    type ReturnSignal,
} from '$lib/utils/tab-return';

const signal = (over: Partial<ReturnSignal> = {}): ReturnSignal => ({
    hidden: false,
    source: 'visible',
    msSinceFrame: 0,
    msSinceReconcile: 60_000,
    msSinceUnpainted: null,
    mayBeBehind: true,
    revalidateOnReturn: false,
    ...over,
});

describe('needsReturnReconcile', () => {
    it('never reconciles while the document is hidden', () => {
        expect(needsReturnReconcile(signal({ hidden: true }))).toBe(false);
        expect(needsReturnReconcile(signal({ hidden: true, msSinceFrame: WAKE_FRAME_GAP_MS }))).toBe(false);
        expect(needsReturnReconcile(signal({ hidden: true, mayBeBehind: false, revalidateOnReturn: true }))).toBe(false);
        expect(needsReturnReconcile(signal({ source: 'resume', hidden: true, revalidateOnReturn: true }))).toBe(false);
    });

    it('never reconciles a screen that cannot be behind', () => {
        expect(needsReturnReconcile(signal({ mayBeBehind: false }))).toBe(false);
        expect(
            needsReturnReconcile(signal({ source: 'focus', msSinceFrame: WAKE_FRAME_GAP_MS, mayBeBehind: false })),
        ).toBe(false);
    });

    it('revalidates every visible return signal when the screen asks, because a missed turn leaves nothing here to prove it', () => {
        // None of the four signals arrives with evidence: the missed turn's frames would have carried it.
        for (const source of ['visible', 'focus', 'pageshow', 'resume'] as const) {
            expect(needsReturnReconcile(signal({
                source, mayBeBehind: false, msSinceFrame: 0, msSinceUnpainted: null, revalidateOnReturn: true,
            }))).toBe(true);
        }
        expect(needsReturnReconcile(signal({
            source: 'visible', mayBeBehind: false, hidden: true, revalidateOnReturn: true,
        }))).toBe(false);
        expect(needsReturnReconcile(signal({
            source: 'focus', mayBeBehind: false, revalidateOnReturn: true, msSinceReconcile: RETURN_DEDUPE_MS - 1,
        }))).toBe(false);
    });

    it('a stopped page does not pull along a screen that answers for itself', () => {
        expect(needsReturnReconcile(signal({ source: 'resume' }))).toBe(false);
        expect(needsReturnReconcile(signal({ source: 'resume', msSinceFrame: WAKE_FRAME_GAP_MS * 4 }))).toBe(false);
        expect(needsReturnReconcile(signal({ source: 'resume', msSinceUnpainted: 1_000 }))).toBe(false);
    });

    it('leaves a window that was never hidden to answer for itself', () => {
        expect(needsReturnReconcile(signal({
            source: 'focus', msSinceFrame: WAKE_FRAME_GAP_MS, mayBeBehind: false,
        }))).toBe(false);
        expect(needsReturnReconcile(signal({
            source: 'pageshow', msSinceFrame: WAKE_FRAME_GAP_MS, mayBeBehind: false,
        }))).toBe(false);
    });

    it('answers a tab becoming visible whatever the socket was doing', () => {
        expect(needsReturnReconcile(signal({ msSinceFrame: 0 }))).toBe(true);
        expect(needsReturnReconcile(signal({ msSinceFrame: null }))).toBe(true);
    });

    it('counts a window regaining focus only after the stream went quiet', () => {
        expect(needsReturnReconcile(signal({ source: 'focus', msSinceFrame: WAKE_FRAME_GAP_MS - 1 }))).toBe(false);
        expect(needsReturnReconcile(signal({ source: 'focus', msSinceFrame: WAKE_FRAME_GAP_MS }))).toBe(true);
        expect(needsReturnReconcile(signal({ source: 'pageshow', msSinceFrame: WAKE_FRAME_GAP_MS * 4 }))).toBe(true);
    });

    it('has no focus evidence at all with no stream to ask', () => {
        expect(needsReturnReconcile(signal({ source: 'focus', msSinceFrame: null }))).toBe(false);
    });

    it('reconciles a window that was never hidden but did miss content', () => {
        // A live socket answers the keepalive every 27 s, so silence cannot reach the gap here.
        expect(needsReturnReconcile(signal({
            source: 'focus', msSinceFrame: 5_000, msSinceUnpainted: 1_000,
        }))).toBe(true);
        expect(needsReturnReconcile(signal({
            source: 'pageshow', msSinceFrame: 5_000, msSinceUnpainted: 1_000,
        }))).toBe(true);
    });

    it('asks nothing of an unfocused window that had nothing to miss', () => {
        expect(needsReturnReconcile(signal({ source: 'focus', msSinceFrame: 5_000 }))).toBe(false);
        expect(needsReturnReconcile(signal({
            source: 'focus', msSinceFrame: 5_000, msSinceUnpainted: 120_000,
        }))).toBe(false);
    });

    it('judges unread content against the reading this view answered last', () => {
        const shared = { source: 'focus' as const, msSinceFrame: 5_000, msSinceUnpainted: 30_000 };
        const reconciledLongAgo = 90_000;
        const reconciledJustNow = 10_000;
        expect(needsReturnReconcile(signal({ ...shared, msSinceReconcile: reconciledLongAgo }))).toBe(true);
        expect(needsReturnReconcile(signal({ ...shared, msSinceReconcile: reconciledJustNow }))).toBe(false);
    });

    it('leaves an unfocused screen that cannot be behind to answer for itself', () => {
        expect(needsReturnReconcile(signal({
            source: 'focus', msSinceFrame: 5_000, msSinceUnpainted: 1_000, mayBeBehind: false,
        }))).toBe(false);
    });

    it('answers one signal per return gesture', () => {
        expect(needsReturnReconcile(signal({ msSinceReconcile: RETURN_DEDUPE_MS - 1 }))).toBe(false);
        expect(needsReturnReconcile(signal({ msSinceReconcile: RETURN_DEDUPE_MS }))).toBe(true);
    });

    it('holds one signal per gesture even against content that arrived after it', () => {
        expect(needsReturnReconcile(signal({
            source: 'focus', msSinceUnpainted: 30_000, msSinceReconcile: RETURN_DEDUPE_MS - 1,
        }))).toBe(false);
        // One suspension can arrive twice: the browser's event, and the clock that noticed it.
        expect(needsReturnReconcile(signal({
            source: 'resume', revalidateOnReturn: true, msSinceReconcile: RETURN_DEDUPE_MS - 1,
        }))).toBe(false);
    });
});
