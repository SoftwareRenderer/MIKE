/**
 * Shared time formatting utilities.
 * Replaces duplicated formatTime/formatTimestamp implementations across components.
 *
 * Everything user-facing here is locale-driven: phrases come from the i18n catalog
 * (or `Intl.RelativeTimeFormat` when the active pack has no key for a rung), and
 * dates/times go through `Intl.DateTimeFormat` on the **active locale** — never the
 * browser default, never a hardcoded English string. See `$lib/i18n/runtime.svelte`.
 */
import { activeLocale, packText, t, type LocaleKey } from '$lib/i18n/runtime.svelte';
import type { MessageKey } from '$lib/i18n/en';

/**
 * Format elapsed milliseconds into a human-readable string (e.g. "6s", "1m 30s").
 * Used by ToolCall for pending tool duration display.
 *
 * Not localized on purpose: "6s"/"1m 30s" are unit abbreviations, not phrases, and
 * the chat stats line (`X tokens · Ys · Z tok/s`) is pinned to this shape.
 */
export function formatElapsed(ms: number): string {
    const totalSec = Math.floor(ms / 1000);
    if (totalSec < 60) return `${totalSec}s`;
    const min = Math.floor(totalSec / 60);
    const sec = totalSec % 60;
    return sec === 0 ? `${min}m` : `${min}m ${sec}s`;
}

/**
 * Format a tool call duration in ms, using seconds when >= 1000ms.
 * Returns e.g. "342ms", "1.2s", "15s".
 *
 * Same reasoning as `formatElapsed`: abbreviations, not phrases.
 */
export function formatDuration(ms: number): string {
    if (ms >= 1000) {
        const totalSec = Math.floor(ms / 1000);
        if (totalSec >= 60) {
            const min = Math.floor(totalSec / 60);
            const sec = totalSec % 60;
            return sec === 0 ? `${min}m` : `${min}m ${sec}s`;
        }
        const sec = ms / 1000;
        if (sec >= 10) return `${Math.round(sec)}s`;
        return `${sec.toFixed(1)}s`;
    }
    return `${ms}ms`;
}

/**
 * How `timeAgo` phrases something older than a day.
 *
 * - `compact` keeps counting: 2w ago, 3mo ago, 1y ago. Right when the *age* is the
 *   fact (a commit lane, a sync indicator).
 * - `calendar` switches to the date itself: "Mar 4". Right when the *moment* is the
 *   fact and "23mo ago" answers a question nobody asked.
 */
export type RelativeTimeScale = 'compact' | 'calendar';

/** Units `Intl.RelativeTimeFormat` knows, i.e. the rungs `timeAgo` can count in. */
type RelativeUnit = 'second' | 'minute' | 'hour' | 'day' | 'week' | 'month' | 'year';

/**
 * "N units ago", in the active locale.
 *
 * Two sources, in this order:
 *
 * 1. The active pack's `time.<unit>Ago` key. It wins because ICU is not always
 *    usable — French narrow renders "-5 min", which is a diff, not a label — so a
 *    pack has to be able to overrule it.
 * 2. `Intl.RelativeTimeFormat(locale, { style: 'narrow' })` otherwise. Narrow is the
 *    style that matches what this file used to hardcode: in `en` it yields exactly
 *    "5m ago" / "3w ago" / "2mo ago". `style: 'long'` would give "5 minutes ago" and
 *    break every pinned assertion — do not "fix" it to long.
 *
 * With no locale chosen the `en` catalog answers (1), so a user with no pack installed
 * sees exactly what they saw before i18n existed.
 */
function relativePhrase(unit: RelativeUnit, n: number): string {
    const fromPack = packText(`time.${unit}Ago`, { n });
    if (fromPack !== undefined) return fromPack;
    return new Intl.RelativeTimeFormat(activeLocale(), { style: 'narrow' }).format(-n, unit);
}

/**
 * "How long ago" for anything that can be read as a moment: an ISO string, an epoch
 * millisecond number, or a Date. Returns '' when the input is not a moment at all.
 *
 * `template` is a **core** catalog key wrapping the phrase around a `{when}` token
 * (`time.built` = "Built {when}") — `MessageKey`-typed, because a template is chosen
 * by the caller in code, never supplied by a pack. A template rather than the old
 * `prefix` string because "Built " is not a PREFIX in every language — the wording
 * and its position belong to the catalog. An unreadable date still answers '' before
 * anything is wrapped, which is what lets callers write
 * `timeAgo(x, { template }) || fallback` without ever rendering a dangling "Built ".
 */
export function timeAgo(
    when: string | number | Date,
    options: { scale?: RelativeTimeScale; template?: MessageKey } = {}
): string {
    const { scale = 'compact', template } = options;
    const date = when instanceof Date ? when : new Date(when);
    if (isNaN(date.getTime())) return '';

    const p = (phrase: string) => (template === undefined ? phrase : t(template, { when: phrase }));
    const diffMs = Date.now() - date.getTime();
    const diffMin = Math.floor(diffMs / 60_000);
    if (diffMin < 1) return p(t('time.justNow'));
    if (diffMin < 60) return p(relativePhrase('minute', diffMin));

    const diffHr = Math.floor(diffMin / 60);
    if (diffHr < 24) return p(relativePhrase('hour', diffHr));

    const diffDay = Math.floor(diffHr / 24);
    if (diffDay < 7) return p(relativePhrase('day', diffDay));

    if (scale === 'calendar') {
        // The year only earns its place when the date is not in the current one.
        const sameYear = date.getFullYear() === new Date().getFullYear();
        return p(new Intl.DateTimeFormat(activeLocale(), sameYear
            ? { month: 'short', day: 'numeric' }
            : { month: 'short', day: 'numeric', year: 'numeric' }).format(date));
    }

    const diffWeek = Math.floor(diffDay / 7);
    if (diffWeek < 5) return p(relativePhrase('week', diffWeek));
    const diffMonth = Math.floor(diffDay / 30);
    if (diffMonth < 12) return p(relativePhrase('month', diffMonth));
    return p(relativePhrase('year', Math.floor(diffDay / 365)));
}

/**
 * Format a relative time string (e.g. "2m ago", "3h ago", "2d ago"), falling back to the
 * date itself past a week. Used by commit lists and the session list.
 */
export function formatRelativeTime(isoStr: string): string {
    if (!isoStr) return '';
    return timeAgo(isoStr, { scale: 'calendar' });
}

/**
 * Format a timestamp for chat message display (e.g. "3:45 PM", "Yesterday at 3:45 PM").
 * Returns `{ display, tooltip }` for inline display + hover tooltip.
 * Used by ChatMessage.
 */
export function formatTimestamp(isoStr: string): { display: string; tooltip: string } {
    const date = new Date(isoStr);
    if (isNaN(date.getTime())) return { display: '', tooltip: '' };

    const now = new Date();
    const todayStart = new Date(now.getFullYear(), now.getMonth(), now.getDate());
    const msgStart = new Date(date.getFullYear(), date.getMonth(), date.getDate());
    const diffMs = todayStart.getTime() - msgStart.getTime();
    const diffDays = Math.round(diffMs / (1000 * 60 * 60 * 24));

    const locale = activeLocale();
    const timeStr = new Intl.DateTimeFormat(locale, { hour: 'numeric', minute: '2-digit' }).format(date);
    const fullDate = new Intl.DateTimeFormat(locale, { month: 'long', day: 'numeric', year: 'numeric' }).format(date);

    if (diffDays === 0) {
        return { display: timeStr, tooltip: '' };
    } else if (diffDays === 1) {
        return { display: t('time.yesterdayAt', { time: timeStr }), tooltip: fullDate };
    } else if (diffDays >= 2 && diffDays <= 7) {
        const weekday = new Intl.DateTimeFormat(locale, { weekday: 'long' }).format(date);
        return { display: t('time.lastWeekdayAt', { weekday, time: timeStr }), tooltip: fullDate };
    } else {
        const shortDate = new Intl.DateTimeFormat(locale, { month: 'short', day: 'numeric', year: 'numeric' }).format(date);
        return { display: t('time.onDateAt', { date: shortDate, time: timeStr }), tooltip: fullDate };
    }
}
