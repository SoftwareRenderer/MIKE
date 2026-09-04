/**
 * The English catalog of record.
 *
 * Every value here is BYTE-IDENTICAL to the string that used to be hardcoded at the
 * call site it came from — `utils/__tests__/format-time.unit.ts` pins several of
 * them, and it is the test that keeps this file honest. A translated pack never
 * lives in core: it is an installed extension (T5), and packs are partial by design.
 *
 * `{placeholder}` tokens are interpolated by `t()` / `packText()`; the token NAMES
 * are part of the key's contract (a pack that drops `{time}` loses the time), so
 * they stay English even in a translated pack.
 */

export type Catalog = Record<string, string>;

export const en = {
    // --- Relative time -----------------------------------------------------
    // The `{n} ago` rungs are here for completeness (and as an escape hatch: a
    // language ICU renders badly in — French narrow is "-5 min" — overrides them).
    // `format-time` prefers Intl.RelativeTimeFormat over these for any locale that
    // does NOT ship the key, which is why they read as abbreviations, not words.
    'time.justNow': 'just now',
    'time.secondAgo': '{n}s ago',
    'time.minuteAgo': '{n}m ago',
    'time.hourAgo': '{n}h ago',
    'time.dayAgo': '{n}d ago',
    'time.weekAgo': '{n}w ago',
    'time.monthAgo': '{n}mo ago',
    'time.yearAgo': '{n}y ago',

    // --- Timestamps (chat) --------------------------------------------------
    'time.yesterdayAt': 'Yesterday at {time}',
    'time.lastWeekdayAt': 'Last {weekday} at {time}',
    'time.onDateAt': '{date} at {time}',

    // --- Wrapped phrases ----------------------------------------------------
    // Templates rather than a `prefix` string, because "Built " is not a prefix in
    // every language — the position belongs to the catalog, not the caller.
    'time.built': 'Built {when}',
    'time.saved': 'Saved {when}',
} satisfies Catalog;

/** Every key the core knows about. A typo in `t('tiem.bulit')` is a type error. */
export type MessageKey = keyof typeof en;
