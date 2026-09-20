import { describe, it, expect } from 'vitest';
import { shouldAutoFullscreenDiffEditor, DIFF_EDITOR_FULLSCREEN_MAX_WIDTH } from '$lib/utils/diff-editor-layout';

describe('shouldAutoFullscreenDiffEditor', () => {
    it('maximizes when the diff column is narrower than the threshold', () => {
        expect(shouldAutoFullscreenDiffEditor(449, false)).toBe(true);
        expect(shouldAutoFullscreenDiffEditor(899, false)).toBe(true);
    });

    it('keeps the editor inline at or above the threshold', () => {
        expect(shouldAutoFullscreenDiffEditor(DIFF_EDITOR_FULLSCREEN_MAX_WIDTH, false)).toBe(false);
        expect(shouldAutoFullscreenDiffEditor(1600, false)).toBe(false);
    });

    it('never maximizes on mobile', () => {
        expect(shouldAutoFullscreenDiffEditor(390, true)).toBe(false);
        expect(shouldAutoFullscreenDiffEditor(0, true)).toBe(false);
        expect(shouldAutoFullscreenDiffEditor(900, true)).toBe(false);
    });

    it('treats an unmeasured (hidden) column as unknown width', () => {
        expect(shouldAutoFullscreenDiffEditor(0, false)).toBe(false);
        expect(shouldAutoFullscreenDiffEditor(-1, false)).toBe(false);
    });

    it('uses the 900px constant', () => {
        expect(DIFF_EDITOR_FULLSCREEN_MAX_WIDTH).toBe(900);
    });
});
