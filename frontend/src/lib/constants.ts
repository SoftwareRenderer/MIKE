// =============================================================================
// Shared constants — magic numbers and strings extracted from components/utils
// =============================================================================

// ─── Scroll Thresholds ───

/** Pixels from bottom of scroll container to consider "at bottom" (auto-scroll). */
export const SCROLL_BOTTOM_THRESHOLD = 32;

// ─── Debounce Intervals ───

/** Delay (ms) before saving draft chat text to IndexedDB. */
export const DRAFT_SAVE_DEBOUNCE_MS = 100;

// ─── WebSocket / Stream Timeouts ───

/** Max idle time (ms) before a WebSocket connection is considered stale (zombie).
 * Server pings at 27s + 10s timeout = ~37s, plus buffer. */
export const STALE_THRESHOLD_MS = 60_000;

/** Interval (ms) between stale-connection heartbeat checks. */
export const HEARTBEAT_INTERVAL_MS = 30_000;

/** Maximum reconnect backoff delay (ms). */
export const RECONNECT_MAX_DELAY_MS = 30_000;

// ─── Cache Sizes ───

/** Max entries in the syntax highlighter cache before eviction. */
export const HIGHLIGHT_CACHE_MAX = 50;

// ─── UI Defaults ───

/** Default toast auto-dismiss duration (ms). */
export const TOAST_DURATION_MS = 4000;
