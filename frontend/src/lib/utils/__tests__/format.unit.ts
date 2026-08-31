import { describe, it, expect } from 'vitest';
import { escapeHtml, formatTokens, formatCost } from '$lib/utils/format';

// Pins the outputs established by the pre-consolidation copies in
// ContextUsage / CompressContextDialog / StatsModal / highlighter.

describe('escapeHtml', () => {
    it('escapes every character that can break out of a text node or attribute', () => {
        expect(escapeHtml(`<img src=x onerror="alert('1')"> & more`))
            .toBe('&lt;img src=x onerror=&quot;alert(&#39;1&#39;)&quot;&gt; &amp; more');
    });

    it('does not double-escape ampersands it introduces', () => {
        expect(escapeHtml(`&'"`)).toBe('&amp;&#39;&quot;');
    });
});

describe('formatTokens', () => {
    it('formats millions with one decimal', () => {
        expect(formatTokens(2_500_000)).toBe('2.5M');
        expect(formatTokens(1_000_000)).toBe('1.0M');
    });

    it('formats thousands with one decimal', () => {
        expect(formatTokens(1234)).toBe('1.2K');
        expect(formatTokens(1_000)).toBe('1.0K');
        expect(formatTokens(999_999)).toBe('1000.0K');
    });

    it('leaves values below 1000 alone', () => {
        expect(formatTokens(0)).toBe('0');
        expect(formatTokens(999)).toBe('999');
    });
});

describe('formatCost', () => {
    // The suite runs under a fixed locale; assert through the same symbol the
    // formatter derives, so the test is locale-robust like the code.
    const symbol = new Intl.NumberFormat(undefined, { style: 'currency', currency: 'USD' })
        .formatToParts(1).find(p => p.type === 'currency')?.value || '$';

    it('uses 4 decimals below $1', () => {
        expect(formatCost(0.123456)).toBe(symbol + '0.1235');
    });

    it('uses 2 decimals at or above $1, with grouping', () => {
        expect(formatCost(1)).toBe(symbol + '1.00');
        expect(formatCost(1234.5)).toBe(symbol + (1234.5).toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 }));
    });

    it('renders non-positive costs as the empty sentinel', () => {
        expect(formatCost(0)).toBe('');
        expect(formatCost(-1)).toBe('');
        expect(formatCost(0, '-')).toBe('-');
    });
});
