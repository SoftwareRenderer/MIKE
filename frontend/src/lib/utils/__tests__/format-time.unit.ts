import { describe, it, expect, afterEach } from 'vitest';
import { formatRelativeTime, formatTimestamp, formatElapsed, formatDuration, timeAgo, dataAgeText } from '$lib/utils/format-time';
import { registerCatalog, setLocale } from '$lib/i18n/runtime.svelte';

const MIN = 60_000, HOUR = 3_600_000, DAY = 86_400_000;
const WEEK = 7 * DAY, MONTH = 30 * DAY, YEAR = 365 * DAY;
const ago = (ms: number) => new Date(Date.now() - ms);

describe('timeAgo', () => {
    it('answers "just now" below a minute', () => {
        expect(timeAgo(ago(30_000))).toBe('just now');
    });

    it('counts minutes, then hours, then days', () => {
        expect(timeAgo(ago(5 * MIN))).toBe('5m ago');
        expect(timeAgo(ago(3 * HOUR))).toBe('3h ago');
        expect(timeAgo(ago(2 * DAY))).toBe('2d ago');
    });

    it('accepts an epoch number, an ISO string and a Date alike', () => {
        const when = ago(10 * MIN);
        expect(timeAgo(when.getTime())).toBe('10m ago');
        expect(timeAgo(when.toISOString())).toBe('10m ago');
        expect(timeAgo(when)).toBe('10m ago');
    });

    it('keeps counting in weeks, months and years on the compact scale', () => {
        expect(timeAgo(ago(3 * WEEK), { scale: 'compact' })).toBe('3w ago');
        expect(timeAgo(ago(4 * WEEK))).toBe('4w ago');
        expect(timeAgo(ago(6 * WEEK))).toBe('1mo ago');
        expect(timeAgo(ago(3 * MONTH))).toBe('3mo ago');
        expect(timeAgo(ago(2 * YEAR))).toBe('2y ago');
    });

    it('switches to the date itself on the calendar scale', () => {
        const result = timeAgo(ago(10 * DAY), { scale: 'calendar' });
        expect(result).not.toContain('ago');
        expect(result).toMatch(/[A-Za-z]+ \d+/);
    });

    it('adds the year to a calendar date from another year', () => {
        expect(timeAgo(new Date(new Date().getFullYear() - 2, 2, 4), { scale: 'calendar' }))
            .toMatch(/\d{4}$/);
    });

    it('wraps every rung in the template, including "just now"', () => {
        expect(timeAgo(ago(30_000), { template: 'time.built' })).toBe('Built just now');
        expect(timeAgo(ago(5 * MIN), { template: 'time.built' })).toBe('Built 5m ago');
    });

    it('answers "" for a bad moment even with a template, so `|| fallback` still works', () => {
        // A caller renders `timeAgo(x, { template }) || t('time.built', { when: raw })`;
        // a dangling "Built " with no date behind it would be non-falsy and win.
        expect(timeAgo('not-a-date', { template: 'time.built' })).toBe('');
        expect(timeAgo(NaN, { template: 'time.built' })).toBe('');
    });

    it('returns an empty string for something that is not a moment', () => {
        // Callers use that as "fall back to whatever raw text I was given".
        expect(timeAgo('not-a-date')).toBe('');
        expect(timeAgo('')).toBe('');
        expect(timeAgo(NaN)).toBe('');
    });
});

describe('dataAgeText', () => {
    it('answers nothing when nothing has landed — never synced is not "just now"', () => {
        // The banner only shows this with no live stream, so "just now" is the one claim it cannot make.
        expect(dataAgeText(null)).toBe('');
        expect(dataAgeText(NaN)).toBe('');
        expect(dataAgeText(Infinity)).toBe('');
    });

    it('ages a board that landed a while ago instead of holding it at "just now"', () => {
        expect(dataAgeText(Date.now() - 30_000)).toBe('just now');
        expect(dataAgeText(Date.now() - 3 * HOUR)).toBe('3h ago');
        expect(dataAgeText(ago(2 * DAY).getTime())).toBe('2d ago');
    });
});

describe('formatRelativeTime', () => {
    it('returns empty string for empty input', () => {
        expect(formatRelativeTime('')).toBe('');
    });

    it('returns empty string for invalid date', () => {
        expect(formatRelativeTime('not-a-date')).toBe('');
    });

    it('returns "just now" for very recent dates', () => {
        const now = new Date().toISOString();
        expect(formatRelativeTime(now)).toBe('just now');
    });

    it('returns minutes ago for recent dates', () => {
        const fiveMinAgo = new Date(Date.now() - 5 * 60 * 1000).toISOString();
        expect(formatRelativeTime(fiveMinAgo)).toBe('5m ago');
    });

    it('returns hours ago for dates within 24h', () => {
        const threeHoursAgo = new Date(Date.now() - 3 * 60 * 60 * 1000).toISOString();
        expect(formatRelativeTime(threeHoursAgo)).toBe('3h ago');
    });

    it('returns days ago for dates within 7 days', () => {
        const twoDaysAgo = new Date(Date.now() - 2 * 24 * 60 * 60 * 1000).toISOString();
        expect(formatRelativeTime(twoDaysAgo)).toBe('2d ago');
    });

    it('returns date string for dates older than 7 days', () => {
        const tenDaysAgo = new Date(Date.now() - 10 * 24 * 60 * 60 * 1000).toISOString();
        const result = formatRelativeTime(tenDaysAgo);
        expect(result).not.toBe('');
        expect(result).not.toContain('ago');
    });
});

describe('formatTimestamp', () => {
    it('returns empty for empty input', () => {
        const result = formatTimestamp('');
        expect(result.display).toBe('');
        expect(result.tooltip).toBe('');
    });

    it('returns empty for invalid date', () => {
        const result = formatTimestamp('not-a-date');
        expect(result.display).toBe('');
        expect(result.tooltip).toBe('');
    });

    it('returns time only for today', () => {
        const now = new Date().toISOString();
        const result = formatTimestamp(now);
        expect(result.display).toMatch(/\d+:\d+ (AM|PM)/);
        expect(result.tooltip).toBe('');
    });

    it('returns "Yesterday at ..." for yesterday', () => {
        const yesterday = new Date(Date.now() - 1 * 24 * 60 * 60 * 1000).toISOString();
        const result = formatTimestamp(yesterday);
        expect(result.display).toMatch(/^Yesterday at \d+:\d+ (AM|PM)$/);
    });

    it('returns "Last WEEKDAY at ..." for 2-7 days ago', () => {
        const threeDaysAgo = new Date(Date.now() - 3 * 24 * 60 * 60 * 1000).toISOString();
        const result = formatTimestamp(threeDaysAgo);
        expect(result.display).toMatch(/^Last \w+ at \d+:\d+ (AM|PM)$/);
    });

    it('returns "Mmm D, YYYY at ..." for older dates', () => {
        const tenDaysAgo = new Date(Date.now() - 10 * 24 * 60 * 60 * 1000).toISOString();
        const result = formatTimestamp(tenDaysAgo);
        expect(result.display).toMatch(/\w+ \d+, \d{4} at \d+:\d+ (AM|PM)/);
    });
});

describe('formatDuration', () => {
    it('shows ms for values under 1000', () => {
        expect(formatDuration(0)).toBe('0ms');
        expect(formatDuration(500)).toBe('500ms');
        expect(formatDuration(999)).toBe('999ms');
    });

    it('shows 1 decimal place for values 1s-9.99s', () => {
        expect(formatDuration(1000)).toBe('1.0s');
        expect(formatDuration(1200)).toBe('1.2s');
        expect(formatDuration(5500)).toBe('5.5s');
        expect(formatDuration(9999)).toBe('10.0s');
    });

    it('shows rounded integer for values >= 10s', () => {
        expect(formatDuration(10000)).toBe('10s');
        expect(formatDuration(15300)).toBe('15s');
        expect(formatDuration(59999)).toBe('60s');
    });

    it('shows minutes for values >= 60s', () => {
        expect(formatDuration(60000)).toBe('1m');
        expect(formatDuration(65000)).toBe('1m 5s');
        expect(formatDuration(120000)).toBe('2m');
        expect(formatDuration(150000)).toBe('2m 30s');
    });
});

describe('formatElapsed', () => {
    it('formats seconds less than 60', () => {
        expect(formatElapsed(0)).toBe('0s');
        expect(formatElapsed(1000)).toBe('1s');
        expect(formatElapsed(55000)).toBe('55s');
    });

    it('formats seconds with minutes and seconds', () => {
        expect(formatElapsed(60000)).toBe('1m');
        expect(formatElapsed(65000)).toBe('1m 5s');
        expect(formatElapsed(90000)).toBe('1m 30s');
    });

    it('formats minutes correctly', () => {
        expect(formatElapsed(120000)).toBe('2m');
        expect(formatElapsed(180000)).toBe('3m');
    });

    it('rounds down partial seconds', () => {
        expect(formatElapsed(1500)).toBe('1s');
        expect(formatElapsed(1999)).toBe('1s');
        expect(formatElapsed(61500)).toBe('1m 1s');
    });
});


// ---------------------------------------------------------------------------
// The active locale, not the browser, drives every phrase above. `en` output is
// pinned by the tests at the top of this file; these pin that CHANGING the locale
// changes the output — which is what "follows the active locale" has to mean.
// Test catalogs are installed through registerCatalog(), the same call an installed
// language pack goes through.
// ---------------------------------------------------------------------------
describe('active locale', () => {
    afterEach(() => setLocale(null));

    it('keeps the pinned English shape when en is the active locale', () => {
        setLocale('en');
        expect(timeAgo(ago(5 * MIN))).toBe('5m ago');
        expect(timeAgo(ago(3 * HOUR))).toBe('3h ago');
        expect(timeAgo(ago(3 * WEEK), { scale: 'compact' })).toBe('3w ago');
        expect(timeAgo(ago(3 * MONTH), { scale: 'compact' })).toBe('3mo ago');
        expect(timeAgo(ago(2 * YEAR), { scale: 'compact' })).toBe('2y ago');
    });

    it('counts in the active locale (narrow ICU phrases, not the English rungs)', () => {
        setLocale('ja');
        expect(timeAgo(ago(5 * MIN))).toBe('5分前');
        expect(timeAgo(ago(3 * MONTH), { scale: 'compact' })).toBe('3か月前');
        setLocale('zh-Hans');
        expect(timeAgo(ago(5 * MIN))).toBe('5分钟前');
        expect(timeAgo(ago(3 * WEEK), { scale: 'compact' })).toBe('3周前');
    });

    it('prefers a pack phrase over ICU, and ICU over English for the rungs it omits', () => {
        // ICU's narrow French is "-5 min" — a diff, not a label — which is exactly why
        // a pack has to be able to overrule it.
        registerCatalog('fr', { 'time.minuteAgo': 'il y a {n} min' });
        setLocale('fr');
        expect(timeAgo(ago(5 * MIN))).toBe('il y a 5 min');
        // The pack has no hour key: ICU French answers, NOT the English "3h ago".
        expect(timeAgo(ago(3 * HOUR))).toBe(
            new Intl.RelativeTimeFormat('fr', { style: 'narrow' }).format(-3, 'hour')
        );
    });

    it('takes "just now" and the chat phrases from the pack — ICU cannot say them', () => {
        registerCatalog('ja', {
            'time.justNow': 'たった今',
            'time.yesterdayAt': '昨日 {time}',
            'time.lastWeekdayAt': '先週{weekday} {time}',
        });
        setLocale('ja');
        expect(timeAgo(ago(30_000))).toBe('たった今');
        const yesterday = formatTimestamp(new Date(Date.now() - DAY).toISOString());
        // 24-hour clock with no AM/PM: the TIME is localized too, not just the words.
        expect(yesterday.display).toMatch(/^昨日 \d{1,2}:\d{2}$/);
        expect(yesterday.display).not.toMatch(/AM|PM/);
        expect(formatTimestamp(new Date(Date.now() - 3 * DAY).toISOString()).display).toMatch(/^先週.+ \d{1,2}:\d{2}$/);
    });

    it('still prints the year only when it is not the current year, in any locale', () => {
        setLocale('ja');
        const year = new Date().getFullYear();
        expect(timeAgo(new Date(year, 2, 4), { scale: 'calendar' })).not.toContain(String(year));
        const other = new Date(year - 2, 2, 4);
        expect(timeAgo(other, { scale: 'calendar' })).toContain(String(other.getFullYear()));
    });

    it('leaves the unit-abbreviation durations alone — the chat stats line is pinned', () => {
        setLocale('ja');
        expect(formatElapsed(65_000)).toBe('1m 5s');
        expect(formatDuration(1200)).toBe('1.2s');
    });
});
