import { describe, it, expect, afterEach } from 'vitest';
import { escapeHtml, formatTokens, formatBytes, formatCost, formatMB, usdSymbol } from '$lib/utils/format';
import { setLocale } from '$lib/i18n/runtime.svelte';

// Pins the outputs established by the pre-consolidation copies in
// ContextUsage / CompressContextDialog / StatsModal / highlighter.

describe('escapeHtml', () => {
    it('escapes every character that can break out of a text node or attribute', () => {
        // Highlight and diff output is interpolated into HTML strings that also carry attributes, so
        // quotes must be escaped too — the one reason this escaper escapes more than a text node needs.
        expect(escapeHtml(`<img src=x onerror="alert('1')"> & more`))
            .toBe('&lt;img src=x onerror=&quot;alert(&#39;1&#39;)&quot;&gt; &amp; more');
    });

    it('does not double-escape ampersands it introduces', () => {
        expect(escapeHtml(`&'"`)).toBe('&amp;&#39;&quot;');
    });
});

describe('formatMB', () => {
    it('prints a memory amount in full, grouped per the active locale', () => {
        // A ceiling an operator compares against installed RAM: compacting it to "13.6K MB" would
        // hide the figure the gate was actually started with.
        setLocale('en');
        expect(formatMB(13624)).toBe('13,624 MB');
        expect(formatMB(900)).toBe('900 MB');
        setLocale('de');
        expect(formatMB(13624)).toBe('13.624 MB');
        setLocale('en');
    });

    it('rounds a fraction rather than printing decimals', () => {
        for (const locale of ['en', 'de'] as const) {
            setLocale(locale);
            expect(formatMB(1023.6)).toBe(new Intl.NumberFormat(locale, { maximumFractionDigits: 0 }).format(1023.6) + ' MB');
        }
        setLocale('en');
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

// ---------------------------------------------------------------------------
// Locale. These are the regression tests for the module-level `usdSymbol` cache,
// which was computed ONCE from `undefined`: the first cost rendered in a session
// pinned the symbol and the separators for every locale after it. The `en` side of
// each assertion is pinned above; what these pin is that switching LOCALE changes
// the output, which a cached value cannot do.
// ---------------------------------------------------------------------------
describe('active locale', () => {
    afterEach(() => setLocale(null));

    it('re-derives grouping and symbol on a locale change (a cached value cannot pass)', () => {
        setLocale('en');
        const en = formatCost(1234.5);
        setLocale('de');
        const de = formatCost(1234.5);
        expect(en).not.toBe(de);
        // Derived through Intl so the assertion does not encode one ICU version's
        // choice of "1.234,50 $" vs "1.234,50 US$" — the point is that it MOVED.
        expect(de).toBe(new Intl.NumberFormat('de', {
            style: 'currency', currency: 'USD', minimumFractionDigits: 2, maximumFractionDigits: 2,
        }).format(1234.5));
        expect(en).toBe('$1,234.50');
    });

    it('keeps the 4-decimals-below-$1 rule in every locale', () => {
        for (const locale of ['en', 'de', 'ja'] as const) {
            setLocale(locale);
            expect(formatCost(0.123456)).toBe(new Intl.NumberFormat(locale, {
                style: 'currency', currency: 'USD', minimumFractionDigits: 4, maximumFractionDigits: 4,
            }).format(0.1235));
        }
    });

    it('reports the symbol for the active locale, not for the first one asked', () => {
        setLocale('en');
        expect(usdSymbol()).toBe('$');
        setLocale('zh-Hans');
        expect(usdSymbol()).toBe(new Intl.NumberFormat('zh-Hans', {
            style: 'currency', currency: 'USD', minimumFractionDigits: 2, maximumFractionDigits: 2,
        }).formatToParts(1).find(p => p.type === 'currency')?.value);
        expect(usdSymbol()).not.toBe('$');
    });

    it('localizes the decimal separator in compact numbers without grouping them', () => {
        // Compact notations carry a scale, not an amount: "1000,0K" is right in de and
        // "1,000.0K" would be wrong in en. That is why useGrouping is off here.
        setLocale('de');
        expect(formatTokens(1234)).toBe('1,2K');
        expect(formatTokens(2_500_000)).toBe('2,5M');
        expect(formatTokens(999_999)).toBe('1000,0K');
        expect(formatBytes(2048)).toBe('2,0 KB');
        setLocale('en');
        expect(formatTokens(1234)).toBe('1.2K');
        expect(formatBytes(2048)).toBe('2.0 KB');
    });

    it('keeps thresholds and rounding identical across locales — only separators move', () => {
        for (const locale of ['en', 'de'] as const) {
            setLocale(locale);
            expect(formatTokens(0)).toBe(new Intl.NumberFormat(locale, { maximumFractionDigits: 0 }).format(0));
            expect(formatTokens(999)).toBe(new Intl.NumberFormat(locale, { maximumFractionDigits: 0 }).format(999));
            expect(formatBytes(900)).toBe(new Intl.NumberFormat(locale, { maximumFractionDigits: 0 }).format(900) + ' B');
            expect(formatTokens(1_000)).toBe(new Intl.NumberFormat(locale, { minimumFractionDigits: 1, maximumFractionDigits: 1 }).format(1) + 'K');
        }
    });
});
