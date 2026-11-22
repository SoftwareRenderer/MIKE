/**
 * What a collapsed reasoning turn is shown while it is still being thought: the text never crossed the
 * wire, so the chat reports how fast it arrived. The token estimate is the one the context donut already
 * prices streamed text with.
 */
import { t } from '$lib/i18n';
import { activeLocale } from '$lib/i18n/runtime.svelte';

const CHARS_PER_TOKEN = 4;

/** Under this much measured wait there is no rate to read: the first count frame carries what the server
 * buffered before it, and the render that shows it follows the frame by about a millisecond. */
const MIN_RATE_WINDOW_MS = 1000;

/** Tokens for N characters of streamed text — an estimate, never a provider's count. */
export function estimatedTokens(chars: number): number {
    return Math.ceil(Math.max(0, chars) / CHARS_PER_TOKEN);
}

/**
 * Average tokens per second over the measured wait, so a pause reads as a slower average rather than a
 * stall. One fraction digit below 10, none above; a wait shorter than `MIN_RATE_WINDOW_MS` answers `''`.
 * The formatter is built per call: a locale cached at module scope outlives a language change.
 */
export function formatReasoningRate(chars: number, elapsedMs: number): string {
    if (elapsedMs < MIN_RATE_WINDOW_MS) return '';
    const seconds = elapsedMs / 1000;
    const rate = estimatedTokens(chars) / seconds;
    const digits = rate > 0 && rate < 10 ? 1 : 0;
    const number = new Intl.NumberFormat(activeLocale(), {
        minimumFractionDigits: digits,
        maximumFractionDigits: digits,
    }).format(rate);
    return t('chat.reasoning.rate', { rate: number });
}
