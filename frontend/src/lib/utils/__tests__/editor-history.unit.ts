import { describe, it, expect } from 'vitest';
import { replayDeltas } from '$lib/utils/editor-history';

const BASE = 'alpha\nbeta\n';

describe('replayDeltas', () => {
    it('answers the base when nothing was recorded', () => {
        expect(replayDeltas(BASE, [])).toBe(BASE);
    });

    it('applies each delta against the text the one before it produced', () => {
        expect(replayDeltas(BASE, [
            { from: 0, to: 5, insert: 'ALPHA' },
            { from: 11, to: 11, insert: 'gamma\n' },
        ])).toBe('ALPHA\nbeta\ngamma\n');
    });

    it('refuses a delta outside the text it was recorded against', () => {
        // A chain saved against another revision of the file: assembling it would paint the wrong bytes.
        expect(replayDeltas(BASE, [{ from: 0, to: 999, insert: 'x' }])).toBeNull();
        expect(replayDeltas(BASE, [{ from: 5, to: 2, insert: 'x' }])).toBeNull();
        expect(replayDeltas('short', [{ from: 9, to: 9, insert: 'x' }])).toBeNull();
    });

    it('refuses the whole chain, not just the bad step', () => {
        expect(replayDeltas(BASE, [
            { from: 0, to: 5, insert: 'ALPHA' },
            { from: 40, to: 40, insert: 'nowhere' },
        ])).toBeNull();
    });
});
