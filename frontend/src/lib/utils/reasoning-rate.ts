/**
 * What a collapsed reasoning turn is shown while it is still being thought: the text never crossed the
 * wire, so the chat reports how fast it arrived. The token estimate is the streamed-text estimate the
 * context donut already uses, so both price the same bytes.
 */
import { t } from '$lib/i18n';
import { activeLocale } from '$lib/i18n/runtime.svelte';

const CHARS_PER_TOKEN = 4;

/** Tokens for N characters of streamed text — an estimate, never a provider's count. */
export function estimatedTokens(chars: number): number {
    return Math.ceil(Math.max(0, chars) / CHARS_PER_TOKEN);
}

/**
 * Average tokens per second over the whole wait, so a pause reads as a slower average rather than a stall.
 * One fraction digit below 10, none above. The formatter is built per call: a locale cached at module scope
 * outlives a language change.
 */
export function formatReasoningRate(chars: number, elapsedMs: number): string {
    const seconds = Math.max(0, elapsedMs) / 1000;
    const rate = seconds <= 0 ? 0 : estimatedTokens(chars) / seconds;
    const digits = rate > 0 && rate < 10 ? 1 : 0;
    const number = new Intl.NumberFormat(activeLocale(), {
        minimumFractionDigits: digits,
        maximumFractionDigits: digits,
    }).format(rate);
    return t('chat.reasoning.rate', { rate: number });
}
