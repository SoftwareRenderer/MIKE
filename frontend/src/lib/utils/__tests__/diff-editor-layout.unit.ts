import { describe, it, expect } from 'vitest';
import { shouldAutoFullscreenDiffEditor, DIFF_EDITOR_FULLSCREEN_MAX_WIDTH } from '$lib/utils/diff-editor-layout';

describe('shouldAutoFullscreenDiffEditor', () => {
    it('maximizes when the diff column is narrower than the threshold', () => {
        expect(shouldAutoFullscreenDiffEditor(449, false, true)).toBe(true);
        expect(shouldAutoFullscreenDiffEditor(899, false, true)).toBe(true);
    });

    it('keeps the editor inline at or above the threshold', () => {
        expect(shouldAutoFullscreenDiffEditor(DIFF_EDITOR_FULLSCREEN_MAX_WIDTH, false, true)).toBe(false);
        expect(shouldAutoFullscreenDiffEditor(1600, false, true)).toBe(false);
    });

    it('never maximizes on mobile', () => {
        expect(shouldAutoFullscreenDiffEditor(390, true, true)).toBe(false);
        expect(shouldAutoFullscreenDiffEditor(0, true, true)).toBe(false);
        expect(shouldAutoFullscreenDiffEditor(900, true, true)).toBe(false);
    });

    it('treats an unmeasured (hidden) column as unknown width', () => {
        expect(shouldAutoFullscreenDiffEditor(0, false, true)).toBe(false);
        expect(shouldAutoFullscreenDiffEditor(-1, false, true)).toBe(false);
    });

    it('leaves the editor inline however narrow, when this browser turned the rule off', () => {
        expect(shouldAutoFullscreenDiffEditor(449, false, false)).toBe(false);
        expect(shouldAutoFullscreenDiffEditor(899, false, false)).toBe(false);
    });

    it('uses the 900px constant', () => {
        expect(DIFF_EDITOR_FULLSCREEN_MAX_WIDTH).toBe(900);
    });
});
