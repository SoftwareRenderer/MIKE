/**
 * Shared text/number formatting helpers.
 *
 * Consolidates implementations that previously lived as byte-identical copies
 * in ContextUsage, CompressContextDialog, StatsModal, sticky-scroll and
 * FileSearchPanel. The canonical escapeHtml here is the strictest of the old
 * copies (it also escapes `'`), so output is safe in attribute context too.
 */

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
    if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + 'M';
    if (n >= 1_000) return (n / 1_000).toFixed(1) + 'K';
    return n.toString();
}

/**
 * Byte count with a binary unit: 900 → "900 B", 2048 → "2.0 KB", 5 MiB → "5.0 MB".
 * Sizes here are file contents, which are counted in 1024-byte blocks, so the units
 * are binary even though the label reads "KB"/"MB".
 */
export function formatBytes(bytes: number): string {
    if (bytes < 1024) return `${bytes} B`;
    if (bytes < 1024 * 1024) return (bytes / 1024).toFixed(1) + ' KB';
    return (bytes / (1024 * 1024)).toFixed(1) + ' MB';
}

/** Cached USD currency symbol for the active locale (e.g. "$", "US$"). */
let usdSymbol: string | null = null;
function localeUsdSymbol(): string {
    if (usdSymbol === null) {
        const parts = new Intl.NumberFormat(undefined, {
            style: 'currency',
            currency: 'USD',
            minimumFractionDigits: 2,
            maximumFractionDigits: 2,
        }).formatToParts(1);
        usdSymbol = parts.find(p => p.type === 'currency')?.value || '$';
    }
    return usdSymbol;
}

/**
 * Format a USD cost with locale-aware grouping: 4 decimals below $1, 2 above.
 * Non-positive costs render as `empty` ("" for the chat badge's "hide the
 * cost entirely" semantics, "-" for the stats table's "no data" cell).
 */
export function formatCost(cost: number, empty = ''): string {
    if (cost <= 0) return empty;
    const digits = cost < 1 ? 4 : 2;
    // toFixed first so the rounding matches the long-standing display, then
    // toLocaleString for grouping separators.
    const formatted = Number(cost.toFixed(digits)).toLocaleString(undefined, {
        minimumFractionDigits: digits,
        maximumFractionDigits: digits,
    });
    return localeUsdSymbol() + formatted;
}
