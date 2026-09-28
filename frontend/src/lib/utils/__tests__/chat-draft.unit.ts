import { describe, it, expect } from 'vitest';
import { draftNeedsSave, draftMayPaint } from '../chat-draft';

// Each case is a shortcut that gets one half of the rule backwards: saving nothing when the box is
// emptied leaves text nobody deleted, and saving the empty box a chat mounts with deletes the draft.

describe('draftNeedsSave', () => {
    it('is true while the box holds something the store does not', () => {
        expect(draftNeedsSave('', 'half-written thought')).toBe(true);
        expect(draftNeedsSave('half-written thought', 'half-written though')).toBe(true);
    });

    it('is true of an emptied box, which is the deletion the store has to learn about', () => {
        expect(draftNeedsSave('text that will be deleted', '')).toBe(true);
    });

    it('is false once the store agrees with the box', () => {
        expect(draftNeedsSave('', '')).toBe(false);
        expect(draftNeedsSave('same text', 'same text')).toBe(false);
    });

    it('is false of the empty box a chat mounts with, before the opening read has answered', () => {
        expect(draftNeedsSave(null, '')).toBe(false);
    });

    it('is true of text typed before that read has answered', () => {
        expect(draftNeedsSave(null, 'typed at once')).toBe(true);
    });
});

describe('draftMayPaint', () => {
    it('is true of the empty box the read opened on', () => {
        expect(draftMayPaint('', false)).toBe(true);
    });

    it('is false of text typed while the read was in flight', () => {
        expect(draftMayPaint('typed at once', false)).toBe(false);
    });

    it('is false once this tab has written the store itself, which decided what it holds', () => {
        expect(draftMayPaint('', true)).toBe(false);
    });
});
