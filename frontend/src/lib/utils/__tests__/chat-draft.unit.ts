import { describe, it, expect } from 'vitest';
import { draftNeedsSave, draftMayPaint, draftAttachmentsNeedSave, draftAttachmentsMayPaint, rememberedAttachments } from '../chat-draft';

// Each case is a shortcut that gets one half of the rule backwards: saving nothing when the box is
// emptied leaves text nobody deleted, and saving the empty box a chat mounts with deletes the draft.

const png = (data: string) => ({ mimeType: 'image/png', data });
const jpeg = (data: string) => ({ mimeType: 'image/jpeg', data });

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
    it('is true of the empty box the read opened on, while nothing is believed yet', () => {
        expect(draftMayPaint('', null, false)).toBe(true);
    });

    it('is false of text typed while the read was in flight', () => {
        expect(draftMayPaint('typed at once', null, false)).toBe(false);
    });

    it('is true of a box that still holds what was believed, where the read brings the store\'s own news', () => {
        expect(draftMayPaint('half-written thought', 'half-written thought', false)).toBe(true);
    });

    it('is false of a box the person emptied since the read began, whose deletion is still waiting on the debounce', () => {
        expect(draftMayPaint('', 'half-written thought', false)).toBe(false);
    });

    it('is false once this tab has written the store itself, which decided what it holds', () => {
        expect(draftMayPaint('', 'half-written thought', true)).toBe(false);
    });

    it('is false once this tab has written, even before any read has answered', () => {
        expect(draftMayPaint('', null, true)).toBe(false);
    });
});

describe('draftAttachmentsNeedSave', () => {
    it('is true while the tray holds images the store does not', () => {
        expect(draftAttachmentsNeedSave([], [png('AAAA')])).toBe(true);
        expect(draftAttachmentsNeedSave([png('AAAA')], [png('AAAA'), png('BBBB')])).toBe(true);
    });

    it('is true of an emptied tray, which is the deletion the store has to learn about', () => {
        expect(draftAttachmentsNeedSave([png('AAAA')], [])).toBe(true);
    });

    it('is true when a kept image is replaced by another, whatever its order in the tray', () => {
        expect(draftAttachmentsNeedSave([png('AAAA'), jpeg('BBBB')], [jpeg('BBBB'), png('AAAA')])).toBe(true);
        expect(draftAttachmentsNeedSave([png('AAAA')], [jpeg('AAAA')])).toBe(true);
    });

    it('is false once the store agrees with the tray', () => {
        expect(draftAttachmentsNeedSave([], [])).toBe(false);
        expect(draftAttachmentsNeedSave([png('AAAA')], [png('AAAA')])).toBe(false);
    });

    it('is false of the empty tray a chat mounts with, before the opening read has answered', () => {
        expect(draftAttachmentsNeedSave(null, [])).toBe(false);
    });

    it('is true of an image attached before that read has answered', () => {
        expect(draftAttachmentsNeedSave(null, [png('AAAA')])).toBe(true);
    });
});

describe('draftAttachmentsMayPaint', () => {
    it('is true of the empty tray the read opened on', () => {
        expect(draftAttachmentsMayPaint([], false)).toBe(true);
    });

    it('is false of an image attached while the read was in flight', () => {
        expect(draftAttachmentsMayPaint([png('AAAA')], false)).toBe(false);
    });

    it('is false once this tab has written the store itself, which is the newer word about what it holds', () => {
        // The read that raced the removal of the last image must not bring it back.
        expect(draftAttachmentsMayPaint([], true)).toBe(false);
    });
});

describe('rememberedAttachments', () => {
    it('holds the images and none of the tray behind them', () => {
        const tray = [png('AAAA')];
        const remembered = rememberedAttachments(tray);
        tray.push(png('BBBB'));
        expect(remembered).toEqual([png('AAAA')]);
    });
});
