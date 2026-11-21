import { describe, it, expect, vi, afterEach } from 'vitest';
import { estimatedTokens, formatReasoningRate } from '$lib/utils/reasoning-rate';
import { activeLocale } from '$lib/i18n/runtime.svelte';

vi.mock('$lib/i18n/runtime.svelte', async (importOriginal) => {
    const real = await importOriginal<typeof import('$lib/i18n/runtime.svelte')>();
    return { ...real, activeLocale: vi.fn(() => 'en') };
});

afterEach(() => vi.mocked(activeLocale).mockReturnValue('en'));

// The rate is an average over the wait, priced by the same estimate the context donut uses.
describe('the rate of a collapsed turn', () => {
    it('counts tokens the way the stream does', () => {
        expect(estimatedTokens(0)).toBe(0);
        expect(estimatedTokens(1)).toBe(1);
        expect(estimatedTokens(4000)).toBe(1000);
        expect(estimatedTokens(-10)).toBe(0);
    });

    it('averages over the whole wait', () => {
        expect(formatReasoningRate(4000, 2000)).toBe('500 tok/s');
        expect(formatReasoningRate(400, 2000)).toBe('50 tok/s');
    });

    it('keeps the fraction while the rate is a trickle, so a warming-up model is not zero', () => {
        expect(formatReasoningRate(40, 20000)).toBe('0.5 tok/s');
        expect(formatReasoningRate(0, 2000)).toBe('0 tok/s');
        expect(formatReasoningRate(100, 0)).toBe('0 tok/s');
    });

    it('renders the number in the active locale and the unit in nobody\'s', () => {
        vi.mocked(activeLocale).mockReturnValue('de');
        expect(formatReasoningRate(40, 20000)).toBe('0,5 tok/s');
    });
});
