import { describe, it, expect } from 'vitest';
import { MODE_DESCRIPTIONS, modeTip } from '$lib/constants/modes';

/**
 * The descriptions render inside the operating-mode selector, and two of the three
 * read sites live in components this batch does not own — so both the copy and the
 * `Record<string, string>` shape are the contract.
 */
describe('MODE_DESCRIPTIONS', () => {
    it('answers every mode with the copy that shipped', () => {
        expect(MODE_DESCRIPTIONS.act).toBe('AI is required to provide structured responses during review, and has read/write access.');
        expect(MODE_DESCRIPTIONS.plan).toBe('AI decomposes work into subtasks on the board, and only has read access to its worktree.');
        expect(MODE_DESCRIPTIONS.explore).toBe('AI has read/write access.');
    });

    it('stays readable through the index signature the call sites use', () => {
        const mode = 'plan';
        expect(MODE_DESCRIPTIONS[mode]).toBe(MODE_DESCRIPTIONS.plan);
        expect(MODE_DESCRIPTIONS['no-such-mode']).toBeUndefined();
    });

    it('reads only the modes that exist (the catalog has no orphan key)', () => {
        expect(Object.keys(MODE_DESCRIPTIONS).sort()).toEqual(['act', 'explore', 'plan']);
    });
});

/** The toggle's tooltip, read by both chat surfaces. */
describe('modeTip', () => {
    it('answers each mode with the tooltip that shipped', () => {
        expect(modeTip('act')).toBe('Act mode: AI is required to provide structured responses during review, and has read/write access. Click to switch.');
        expect(modeTip('plan')).toBe('Plan mode: AI decomposes work into subtasks on the board, and only has read access to its worktree. Click to switch.');
        expect(modeTip('explore')).toBe('Explore mode: AI has read/write access. Click to switch.');
    });

    it('answers a mode it does not know with the explore tip', () => {
        expect(modeTip('no-such-mode')).toBe(modeTip('explore'));
    });
});
