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
    // One decision: where the compact scale starts, and what it prints above it.
    it.each([
        ['below the first threshold', 0, '0'],
        ['below the first threshold', 999, '999'],
        ['thousands, one decimal', 1_000, '1.0K'],
        ['thousands, one decimal', 1234, '1.2K'],
        // The rung below a million still carries the K, however large the mantissa gets.
        ['thousands, one decimal', 999_999, '1000.0K'],
        ['millions, one decimal', 1_000_000, '1.0M'],
        ['millions, one decimal', 2_500_000, '2.5M'],
    ])('formats %s', (_rung, value, expected) => {
        expect(formatTokens(value)).toBe(expected);
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

    it('localizes the separators of a compact number without grouping or moving a threshold', () => {
        // Compact notations carry a scale, not an amount: "1000,0K" is right in de and
        // "1,000.0K" would be wrong in en. That is why useGrouping is off here, and why the
        // thresholds, units and rounding below are the ones pinned in `en` — only separators move.
        for (const [locale, digits, compact, kb, bytes] of [
            ['en', ['0', '999'], ['1.0K', '1.2K', '2.5M', '1000.0K'], '2.0 KB', '900 B'],
            ['de', ['0', '999'], ['1,0K', '1,2K', '2,5M', '1000,0K'], '2,0 KB', '900 B'],
        ] as const) {
            setLocale(locale);
            expect([formatTokens(0), formatTokens(999)]).toEqual(digits);
            expect([
                formatTokens(1_000), formatTokens(1234), formatTokens(2_500_000), formatTokens(999_999),
            ]).toEqual(compact);
            expect(formatBytes(2048)).toBe(kb);
            expect(formatBytes(900)).toBe(bytes);
        }
        setLocale('en');
    });
});
