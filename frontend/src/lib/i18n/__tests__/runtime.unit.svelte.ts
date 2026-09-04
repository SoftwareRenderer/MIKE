import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { render, cleanup } from '@testing-library/svelte';
import { tick } from 'svelte';
import { t, setCatalog, resetToEnglish } from '$lib/i18n';
import type { MessageKey, MessageValue, PluralTable } from '$lib/i18n';
import LocaleProbe from './LocaleProbe.svelte';

/**
 * The reactive half of the runtime, which is the half a conversion batch cannot test: locale
 * and catalogs live in `$state`, so enabling, replacing or removing a pack must re-render
 * every reader — a `$derived` and a rendered template — with no reload, no re-created
 * component and no version bumper.
 *
 * This file is `.svelte.ts` because it uses runes of its own; the deterministic resolution
 * surface (interpolation, plural categories, the fallback ladder, `<html lang>`) is asserted
 * in `runtime.unit.ts`.
 */

/** Locale fixtures named for the plural categories they answer with — see runtime.unit.ts. */
const TWO_FORMS = 'de';
const MANY_FORMS = 'ru';

/** A pack fixture typed by key: a typo is a TS error, not a silently untranslated key. */
function pack(entries: { [K in MessageKey]?: MessageValue }): Record<string, MessageValue> {
    const out: Record<string, MessageValue> = {};
    for (const [key, value] of Object.entries(entries)) {
        if (value !== undefined) out[key] = value;
    }
    return out;
}

/** Synthetic forms — a category names itself, and no translated string enters the repo. */
const MANY_FORM_TABLE: PluralTable = {
    one: '[one:{count}]',
    few: '[few:{count}]',
    many: '[many:{count}]',
    other: '[other:{count}]',
};

// Readers at module scope: each is a live view over the runtime's `$state`, read here outside
// any effect scope (lazily recomputed when its dependencies change).
const derivedPlain = $derived(t('i18n.test.plain'));
const derivedFiles = $derived(t('i18n.test.files', { count: 3 }));

let warn: ReturnType<typeof vi.spyOn>;

beforeEach(() => {
    resetToEnglish();
    warn = vi.spyOn(console, 'warn').mockImplementation(() => { });
});

afterEach(() => {
    warn.mockRestore();
    resetToEnglish();
    cleanup();
});

describe('a $derived reader', () => {
    it('changes when a pack is installed for the first time', () => {
        expect(derivedPlain).toBe('Plain message');

        setCatalog(TWO_FORMS, pack({ 'i18n.test.plain': '[translated]' }));
        expect(derivedPlain).toBe('[translated]');
    });

    it('changes when the active pack is replaced live — the loading task\'s swap', () => {
        setCatalog(TWO_FORMS, pack({ 'i18n.test.plain': '[first]' }));
        expect(derivedPlain).toBe('[first]');

        // Same locale, new messages: nothing uninstalls, nothing reloads.
        setCatalog(TWO_FORMS, pack({ 'i18n.test.plain': '[second]' }));
        expect(derivedPlain).toBe('[second]');
    });

    it('switches plural forms with the locale, not with the catalog it read last', () => {
        expect(derivedFiles).toBe('3 files');

        setCatalog(MANY_FORMS, pack({ 'i18n.test.files': MANY_FORM_TABLE }));
        expect(derivedFiles).toBe('[few:3]');

        // The next pack declares only `other`: the same reader degrades, it does not blank.
        setCatalog(TWO_FORMS, pack({ 'i18n.test.files': { other: '[other:{count}]' } }));
        expect(derivedFiles).toBe('[other:3]');
    });

    it('goes back to English when a pack is dropped, including for keys it never had', () => {
        setCatalog(TWO_FORMS, pack({ 'i18n.test.plain': '[translated]' }));
        expect(derivedPlain).toBe('[translated]');
        // Not translated by that pack — English, all the way through.
        expect(derivedFiles).toBe('3 files');

        resetToEnglish();
        expect(derivedPlain).toBe('Plain message');
        expect(derivedFiles).toBe('3 files');
    });

    it('reports each untranslated key once across all of these swaps', () => {
        setCatalog(TWO_FORMS, pack({}));
        expect(derivedPlain).toBe('Plain message');
        expect(derivedFiles).toBe('3 files');
        expect(warn.mock.calls.length).toBe(2);

        // Reading them again (the way a re-render would) adds nothing to the console.
        expect(derivedPlain).toBe('Plain message');
        expect(derivedFiles).toBe('3 files');
        expect(warn.mock.calls.length).toBe(2);
    });
});

describe('a rendered template', () => {
    it('re-renders on a live catalog swap with no re-render and no reload', async () => {
        const { container } = render(LocaleProbe, { count: 2 });
        const probe = () => container.querySelector('.probe')?.textContent;

        expect(probe()).toBe('2 files');

        setCatalog(MANY_FORMS, pack({ 'i18n.test.files': MANY_FORM_TABLE }));
        await tick();
        expect(probe()).toBe('[few:2]');

        // A replacement pack for the same locale — the live swap again, same component tree.
        setCatalog(MANY_FORMS, pack({ 'i18n.test.files': { other: '[swapped:{count}]' } }));
        await tick();
        expect(probe()).toBe('[swapped:2]');

        resetToEnglish();
        await tick();
        expect(probe()).toBe('2 files');
    });

    it('keeps showing English for a key the installed pack lacks', async () => {
        const { container } = render(LocaleProbe, { count: 2 });

        setCatalog(TWO_FORMS, pack({ 'i18n.test.plain': '[not the key this probe reads]' }));
        await tick();

        expect(container.querySelector('.probe')?.textContent).toBe('2 files');
        expect(document.documentElement.lang).toBe(TWO_FORMS);
    });
});
