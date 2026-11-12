import { describe, it, expect, afterEach } from 'vitest';
import { formatQueueWait } from '$lib/utils/queue-wait';
import { registerCatalog, setLocale } from '$lib/i18n/runtime.svelte';

describe('formatQueueWait', () => {
    afterEach(() => setLocale(null));

    it('counts a wait of a minute or less in seconds', () => {
        expect(formatQueueWait(0)).toBe('~0s');
        expect(formatQueueWait(45)).toBe('~45s');
        expect(formatQueueWait(60)).toBe('~60s');
    });

    it('counts a longer wait in minutes, singular at one and plural above it', () => {
        expect(formatQueueWait(61)).toBe('~1 minute');
        expect(formatQueueWait(120)).toBe('~2 minutes');
        expect(formatQueueWait(180)).toBe('~3 minutes');
    });

    it('rounds to the nearest minute, because the estimate is an average slot duration', () => {
        expect(formatQueueWait(90)).toBe('~2 minutes');
        expect(formatQueueWait(89)).toBe('~1 minute');
    });

    it('takes the plural form from the locale, so a pack shipping `other` alone answers one too', () => {
        // 61 selects `one` in English; a language with a single form declares `other` alone
        // and must still answer, rather than falling back to the English rung.
        registerCatalog('ja', { 'chat.queue.waitMinutes': { other: '約{count}分' } });
        setLocale('ja');
        expect(formatQueueWait(61)).toBe('約1分');
    });
});
