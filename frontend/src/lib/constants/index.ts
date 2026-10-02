// =============================================================================
// Shared constants — magic numbers and strings extracted from components/utils
//
// Nothing here imports `$lib/i18n`, and `./modes` is not re-exported: readers of these numbers (the
// stream, the terminal, the highlighter) must not have to evaluate the catalog to get a timeout.
// Mode copy is `$lib/constants/modes`; the stacking tiers are `$lib/constants/z-layers`.
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

/** First reconnect delay (ms); every unanswered attempt doubles from here. */
export const RECONNECT_BASE_DELAY_MS = 1_000;

/** Maximum reconnect backoff delay (ms). It is the pace for a server that is genuinely down while
 * this page's timers are running — see `SUSPENDED_GAP_MS`. */
export const RECONNECT_MAX_DELAY_MS = 30_000;

/** A gap this long between two of the stream's own timer ticks means the page was not running
 * between them — sleep, hidden-tab throttling, a frozen page — rather than merely busy: a page that
 * is running fires within milliseconds of the delay it asked for. */
export const SUSPENDED_GAP_MS = 10_000;

/** First delay (ms) before the offline queue replays again after one of its own sends failed. Each
 * failure doubles it, up to `RECONNECT_MAX_DELAY_MS`. */
export const QUEUE_RETRY_BASE_DELAY_MS = 1_000;

/** First wait (ms) before a file save that got no answer is written again; each failure doubles it, up to
 * `SAVE_RETRY_MAX_DELAY_MS`. */
export const SAVE_RETRY_BASE_DELAY_MS = 2_000;

/** The longest that wait gets. It is a pace, not a deadline. */
export const SAVE_RETRY_MAX_DELAY_MS = 15_000;

// ─── UI Defaults ───

/** Default toast auto-dismiss duration (ms). */
export const TOAST_DURATION_MS = 4000;
