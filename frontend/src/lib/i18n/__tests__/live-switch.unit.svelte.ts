import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { i18n, t } from '$lib/i18n/runtime.svelte';
import { DEFAULT_LOCALE } from '$lib/i18n/en';

// Component-level proof that the live switch is the runtime's `$state` and
// nothing else. A component's markup is a `$derived` over `t()` as far as Svelte
// is concerned, so a `$derived` consumer here re-evaluating when a pack arrives
// or leaves *is* the re-render — no reload, no version bumper, no `$effect`
// mirror (any of which would be a second source of truth for the language).
//
// The catalog is written once, by the loader, on the exported singleton — which
// is the same object every consumer below reads.

/** Stand-in for a component: one reactive read of one message id. */
class Label {
    text = $derived(i18n.t('nav.board'));
}

class Footer {
    text = $derived(t('footer.status'));
}

describe('i18n live switch (reactive consumers)', () => {
    // Some cases below deliberately render a missing key; the runtime's warn is
    // covered in runtime.unit.svelte.ts, not here.
    let warn: ReturnType<typeof vi.spyOn>;

    beforeEach(() => {
        warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
        i18n.setCatalog(DEFAULT_LOCALE, null);
    });

    afterEach(() => {
        warn.mockRestore();
    });

    it('re-renders a consumer when a pack is enabled, with no reload', () => {
        const label = new Label();
        expect(label.text).toBe('nav.board'); // English: no pack, no key in `en`

        i18n.setCatalog('zh-CN', { 'nav.board': '看板' });
        expect(label.text).toBe('看板');
    });

    it('re-renders again when the pack is disabled', () => {
        const label = new Label();
        i18n.setCatalog('zh-CN', { 'nav.board': '看板' });
        expect(label.text).toBe('看板');

        i18n.setCatalog('zh-CN', null);
        expect(label.text).toBe('nav.board');
        expect(i18n.locale).toBe(DEFAULT_LOCALE);
    });

    it('updates every consumer at once, from the one runtime', () => {
        const label = new Label();
        const footer = new Footer();
        i18n.setCatalog('fr', { 'nav.board': 'Tableau', 'footer.status': 'Prêt' });
        expect(label.text).toBe('Tableau');
        expect(footer.text).toBe('Prêt');

        // Swapping packs (the two-enabled-packs case) is the same single write.
        i18n.setCatalog('ja', { 'nav.board': 'ボード', 'footer.status': '準備完了' });
        expect(label.text).toBe('ボード');
        expect(footer.text).toBe('準備完了');
    });

    it('keeps a consumer on English per key when the new pack lacks the key', () => {
        const label = new Label();
        i18n.setCatalog('zh-CN', { 'nav.board': '看板' });
        i18n.setCatalog('de', {}); // the German pack does not translate it
        expect(label.text).toBe('nav.board');
        expect(i18n.locale).toBe('de');
    });
});
