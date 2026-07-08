/**
 * Shared time formatting utilities.
 * Replaces duplicated formatTime/formatTimestamp implementations across components.
 */

/**
 * Format a relative time string (e.g. "2m ago", "3h ago", "2d ago").
 * Used by CardDetail for commit timestamps.
 */
export function formatRelativeTime(isoStr: string): string {
    if (!isoStr) return '';
    const date = new Date(isoStr);
    if (isNaN(date.getTime())) return '';
    const now = new Date();
    const diffMs = now.getTime() - date.getTime();
    const diffMin = Math.floor(diffMs / 60000);
    if (diffMin < 1) return 'just now';
    if (diffMin < 60) return `${diffMin}m ago`;
    const diffHr = Math.floor(diffMin / 60);
    if (diffHr < 24) return `${diffHr}h ago`;
    const diffDay = Math.floor(diffHr / 24);
    if (diffDay < 7) return `${diffDay}d ago`;
    return date.toLocaleDateString([], { month: 'short', day: 'numeric' });
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

/**
 * Format a simple time string (e.g. "3:45 PM").
 * Used by CheckpointDivider for thread timestamps.
 */
export function formatTime(isoStr: string): string {
    if (!isoStr) return '';
    const date = new Date(isoStr);
    if (isNaN(date.getTime())) return '';
    return date.toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' });
}
