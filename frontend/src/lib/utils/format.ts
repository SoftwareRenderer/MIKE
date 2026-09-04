/**
 * Shared text/number formatting helpers.
 *
 * Consolidates implementations that previously lived as byte-identical copies
 * in ContextUsage, CompressContextDialog, StatsModal, sticky-scroll and
 * FileSearchPanel. The canonical escapeHtml here is the strictest of the old
 * copies (it also escapes `'`), so output is safe in attribute context too.
 *
 * Number formatting here follows the ACTIVE locale (`$lib/i18n/runtime.svelte`), never
 * the browser default and never a value cached against an older locale. Currency stays
 * USD throughout — this is grouping and symbol shape, not currency conversion.
 */
import { activeLocale } from '$lib/i18n/runtime.svelte';

/**
 * Escape HTML special characters in a string.
 *
 * Quotes are included so the result is safe in an attribute as well as a
 * text node — callers build HTML strings that carry `class="…"` attributes
 * next to the escaped content.
 */
export function escapeHtml(text: string): string {
    return text
        .replace(/&/g, '&amp;')
        .replace(/</g, '&lt;')
        .replace(/>/g, '&gt;')
        .replace(/"/g, '&quot;')
        .replace(/'/g, '&#39;');
}

/** Compact token count: 1234 → "1.2K", 2500000 → "2.5M", 999 → "999". */
export function formatTokens(n: number): string {
    if (n >= 1_000_000) return compactNumber(n / 1_000_000, 1) + 'M';
    if (n >= 1_000) return compactNumber(n / 1_000, 1) + 'K';
    return compactNumber(n, 0);
}

/**
 * Byte count with a binary unit: 900 → "900 B", 2048 → "2.0 KB", 5 MiB → "5.0 MB".
 * Sizes here are file contents, which are counted in 1024-byte blocks, so the units
 * are binary even though the label reads "KB"/"MB".
 */
export function formatBytes(bytes: number): string {
    if (bytes < 1024) return `${compactNumber(bytes, 0)} B`;
    if (bytes < 1024 * 1024) return `${compactNumber(bytes / 1024, 1)} KB`;
    return `${compactNumber(bytes / (1024 * 1024), 1)} MB`;
}

/**
 * A number rendered in the active locale WITHOUT digit grouping, at `digits` decimals.
 *
 * This is what makes `formatTokens`/`formatBytes` locale-aware without changing their
 * shape: the decimal separator localizes ("1,2K" in `de`) while the mantissa stays a
 * scale, not an amount — grouping inside a compact number ("1,000.0K") would be wrong,
 * and `999_999 → "1000.0K"` is a pinned output. The thresholds and the `toFixed` rounding
 * are the long-standing ones; `toFixed` first, then re-print, so no second rounding step
 * can move a value a tenth.
 */
function compactNumber(value: number, digits: number): string {
    return Number(value.toFixed(digits)).toLocaleString(activeLocale(), {
        minimumFractionDigits: digits,
        maximumFractionDigits: digits,
        useGrouping: false,
    });
}

/**
 * The USD symbol for the active locale ("$", "US$", "$US") — the bare symbol, for
 * decorating a form field. `formatCost` does NOT use this: it lets ICU place the
 * symbol, which puts it AFTER the amount in German and French.
 *
 * The cache is keyed by the locale it was derived from, so it cannot go stale. It used
 * to be one module-level `let` computed once from `undefined`, which was harmless only
 * while the locale could not change: the first call in a session pinned "$" for a user
 * who then switched language.
 */
const usdSymbolByLocale = new Map<string | undefined, string>();
export function usdSymbol(): string {
    const locale = activeLocale();
    const cached = usdSymbolByLocale.get(locale);
    if (cached !== undefined) return cached;
    const parts = new Intl.NumberFormat(locale, {
        style: 'currency',
        currency: 'USD',
        minimumFractionDigits: 2,
        maximumFractionDigits: 2,
    }).formatToParts(1);
    const symbol = parts.find(p => p.type === 'currency')?.value || '$';
    usdSymbolByLocale.set(locale, symbol);
    return symbol;
}

/**
 * Format a USD cost with locale-aware grouping: 4 decimals below $1, 2 above.
 * Non-positive costs render as `empty` ("" for the chat badge's "hide the
 * cost entirely" semantics, "-" for the stats table's "no data" cell).
 *
 * One ICU currency format does the whole job — symbol, placement, grouping, decimal
 * separator — because assembling them by hand is how a prefix-only symbol ends up in
 * front of an amount in a language that writes it after. In `en` the output is
 * byte-identical to the old `symbol + toLocaleString(...)`; the digits rule is applied
 * through `Intl` so the rounding stays the long-standing one (`toFixed` first, then
 * print at exactly that many decimals).
 */
export function formatCost(cost: number, empty = ''): string {
    if (cost <= 0) return empty;
    const digits = cost < 1 ? 4 : 2;
    return new Intl.NumberFormat(activeLocale(), {
        style: 'currency',
        currency: 'USD',
        minimumFractionDigits: digits,
        maximumFractionDigits: digits,
    }).format(Number(cost.toFixed(digits)));
}
