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
    mayBeBehind: true,
    ...over,
});

describe('needsReturnReconcile', () => {
    it('never reconciles while the document is hidden', () => {
        expect(needsReturnReconcile(signal({ hidden: true }))).toBe(false);
        expect(needsReturnReconcile(signal({ hidden: true, msSinceFrame: WAKE_FRAME_GAP_MS }))).toBe(false);
    });

    it('never reconciles a screen that cannot be behind', () => {
        expect(needsReturnReconcile(signal({ mayBeBehind: false }))).toBe(false);
        expect(
            needsReturnReconcile(signal({ source: 'focus', msSinceFrame: WAKE_FRAME_GAP_MS, mayBeBehind: false })),
        ).toBe(false);
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

    it('answers one signal per return gesture', () => {
        expect(needsReturnReconcile(signal({ msSinceReconcile: RETURN_DEDUPE_MS - 1 }))).toBe(false);
        expect(needsReturnReconcile(signal({ msSinceReconcile: RETURN_DEDUPE_MS }))).toBe(true);
    });
});
