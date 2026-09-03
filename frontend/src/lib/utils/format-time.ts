/**
 * Shared time formatting utilities.
 * Replaces duplicated formatTime/formatTimestamp implementations across components.
 */

/**
 * Format elapsed milliseconds into a human-readable string (e.g. "6s", "1m 30s").
 * Used by ToolCall for pending tool duration display.
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

/**
 * "How long ago" for anything that can be read as a moment: an ISO string, an epoch
 * millisecond number, or a Date. Returns '' when the input is not a moment at all.
 *
 * `prefix` is prepended to every phrase ("Built ", "Saved ") so callers never have to
 * string-join around it and get a doubled space on the "just now" rung.
 */
export function timeAgo(when: string | number | Date, options: { scale?: RelativeTimeScale; prefix?: string } = {}): string {
    const { scale = 'compact', prefix = '' } = options;
    const date = when instanceof Date ? when : new Date(when);
    if (isNaN(date.getTime())) return '';

    const p = (phrase: string) => prefix + phrase;
    const diffMs = Date.now() - date.getTime();
    const diffMin = Math.floor(diffMs / 60_000);
    if (diffMin < 1) return p('just now');
    if (diffMin < 60) return p(`${diffMin}m ago`);

    const diffHr = Math.floor(diffMin / 60);
    if (diffHr < 24) return p(`${diffHr}h ago`);

    const diffDay = Math.floor(diffHr / 24);
    if (diffDay < 7) return p(`${diffDay}d ago`);

    if (scale === 'calendar') {
        // The year only earns its place when the date is not in the current one.
        const sameYear = date.getFullYear() === new Date().getFullYear();
        return p(date.toLocaleDateString([], sameYear
            ? { month: 'short', day: 'numeric' }
            : { month: 'short', day: 'numeric', year: 'numeric' }));
    }

    const diffWeek = Math.floor(diffDay / 7);
    if (diffWeek < 5) return p(`${diffWeek}w ago`);
    const diffMonth = Math.floor(diffDay / 30);
    if (diffMonth < 12) return p(`${diffMonth}mo ago`);
    return p(`${Math.floor(diffDay / 365)}y ago`);
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

    const timeStr = date.toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' });
    const fullDate = date.toLocaleDateString([], { month: 'long', day: 'numeric', year: 'numeric' });

    if (diffDays === 0) {
        return { display: timeStr, tooltip: '' };
    } else if (diffDays === 1) {
        return { display: `Yesterday at ${timeStr}`, tooltip: fullDate };
    } else if (diffDays >= 2 && diffDays <= 7) {
        const weekday = date.toLocaleDateString([], { weekday: 'long' });
        return { display: `Last ${weekday} at ${timeStr}`, tooltip: fullDate };
    } else {
        const shortDate = date.toLocaleDateString([], { month: 'short', day: 'numeric', year: 'numeric' });
        return { display: `${shortDate} at ${timeStr}`, tooltip: fullDate };
    }
}


