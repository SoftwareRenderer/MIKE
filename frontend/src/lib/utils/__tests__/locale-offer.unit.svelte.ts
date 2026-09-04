import { describe, it, expect } from 'vitest';
import type { Extension, RegistryEntry } from '$lib/types';
import {
    LOCALE_OFFER_SETTING_KEY,
    entryDisplayName,
    primarySubtag,
    resolveLocaleOffer,
    type LocaleOfferOutcome,
    type LocaleOfferReason,
    type LocaleOfferRecord,
} from '../locale-offer';

// The whole first-run policy is here, and none of it needs a browser, a server or
// a registry: the resolver is given the browser language, the index, the installed
// extensions and the record, and answers offer/no-offer-with-a-reason. The four
// "must hold" cases and every "say nothing" case are each one test.
//
// `.svelte.ts` because the resolver reuses `resolveI18NPack`, whose module graph
// reaches the i18n runtime's `$state`.

function makePack(opts: {
    name: string;
    locale?: string;
    author?: string;
    enabled?: boolean;
    type?: Extension['type'];
    /** Pass false to build an extension that declares no i18n capability. */
    declare?: boolean;
}): Extension {
    return {
        id: opts.name,
        name: opts.name,
        // Identity everywhere is `<author-slug>/<name>` off these two fields (not
        // `manifest.author`), so a fixture that must collide with a registry entry
        // sets it here.
        author: opts.author ?? 'tester',
        version: '1.0.0',
        type: opts.type ?? 'i18n',
        enabled: opts.enabled ?? true,
        manifest: {
            display_name: opts.name,
            description: 'test pack',
            author: opts.author ?? 'tester',
            version: '1.0.0',
            capabilities:
                opts.declare === false
                    ? {}
                    : { i18n: { locale: opts.locale ?? 'ja', messages_url: 'messages.json' } },
        },
        config: {},
        container_config: {},
        sandbox_config: { network_isolation: false, memory_limit: 0 },
        running: false,
        installed_at: '',
        built_at: '',
        install_source: 'registry',
    };
}

function makeEntry(opts: {
    name: string;
    locale?: string;
    author?: string;
    display_name?: string;
    repo?: string;
    commit?: string;
    type?: RegistryEntry['type'];
}): RegistryEntry {
    return {
        name: opts.name,
        version: '1.0.0',
        type: opts.type ?? 'i18n',
        display_name: opts.display_name ?? opts.name,
        description: 'test pack',
        author: opts.author ?? 'Tester',
        repo: opts.repo === undefined ? 'https://example.test/extensions' : opts.repo,
        commit: opts.commit === undefined ? '0'.repeat(40) : opts.commit,
        subdir: `i18n/${opts.name}`,
        capabilities:
            opts.locale === undefined ? {} : { i18n: { locale: opts.locale, messages_url: 'messages.json' } },
    };
}

function record(outcome: LocaleOfferOutcome): LocaleOfferRecord {
    return { pack: 'tester/locale-ja', outcome };
}

describe('primarySubtag', () => {
    it('takes the primary subtag of a tag, case-folded', () => {
        expect(primarySubtag('zh-CN')).toBe('zh');
        expect(primarySubtag('zh-Hans-CN')).toBe('zh');
        expect(primarySubtag('de-AT')).toBe('de');
        expect(primarySubtag('ZH')).toBe('zh');
        // Some platforms spell the separator with an underscore.
        expect(primarySubtag('fr_FR')).toBe('fr');
    });

    it('reads garbage as "no language"', () => {
        for (const bad of ['', '   ', '*', undefined, null, 42, 'x-pig', '1234', 'e', 'deutschland-language']) {
            expect(primarySubtag(bad)).toBe('');
        }
    });
});

describe('resolveLocaleOffer — when it offers', () => {
    it('offers the matching pack when no pack is enabled and nothing was asked', () => {
        const entry = makeEntry({ name: 'locale-ja', locale: 'ja', display_name: '日本語' });
        const decision = resolveLocaleOffer({
            browserLanguage: 'ja',
            registry: [entry],
            extensions: [],
            record: null,
        });
        expect(decision.offer).toBe(true);
        if (!decision.offer) return;
        expect(decision.entry).toBe(entry);
        expect(decision.locale).toBe('ja');
        expect(decision.displayName).toBe('日本語');
    });

    it('matches a regional browser tag against a base-tag pack', () => {
        const decision = resolveLocaleOffer({
            browserLanguage: 'de-AT',
            registry: [makeEntry({ name: 'locale-de', locale: 'de' })],
            // An enabled *lsp* extension is not a pack: only an enabled i18n
            // declaration counts as "there is already a language".
            extensions: [makePack({ name: 'some-lsp', declare: false, type: 'lsp', enabled: true })],
            record: null,
        });
        expect(decision.offer).toBe(true);
    });

    it('prefers the exact tag over a primary-subtag match', () => {
        const base = makeEntry({ name: 'locale-zh', locale: 'zh', display_name: '中文' });
        const exact = makeEntry({ name: 'locale-zh-cn', locale: 'zh-CN', display_name: '简体中文' });
        const decision = resolveLocaleOffer({
            browserLanguage: 'zh-CN',
            registry: [base, exact],
            extensions: [],
            record: null,
        });
        if (!decision.offer) throw new Error('expected an offer');
        expect(decision.entry).toBe(exact);
        expect(decision.locale).toBe('zh-CN');
    });

    it('breaks ties on the name, not on the order the registry answered in', () => {
        const a = makeEntry({ name: 'aaa-ko', locale: 'ko' });
        const b = makeEntry({ name: 'bbb-ko', locale: 'ko' });
        const first = resolveLocaleOffer({ browserLanguage: 'ko-KR', registry: [b, a], extensions: [], record: null });
        const second = resolveLocaleOffer({ browserLanguage: 'ko-KR', registry: [a, b], extensions: [], record: null });
        if (!first.offer || !second.offer) throw new Error('expected offers');
        expect(first.entry.name).toBe('aaa-ko');
        expect(second.entry).toBe(first.entry);
    });
});

describe('resolveLocaleOffer — when it says nothing', () => {
    const matching = [makeEntry({ name: 'locale-ja', locale: 'ja', display_name: '日本語' })];
    const ask = (over: Partial<Parameters<typeof resolveLocaleOffer>[0]>) =>
        resolveLocaleOffer({
            browserLanguage: 'ja',
            registry: matching,
            extensions: [],
            record: null,
            ...over,
        });

    /**
     * Why a case was refused. `LocaleOfferDecision` is a discriminated union, and
     * `.reason` on the union is not something TypeScript narrows through an
     * expectation — so the narrowing happens here, where an offer that should not
     * have happened fails loudly instead of reading as `undefined`.
     */
    const whyNot = (over: Parameters<typeof ask>[0] = {}): LocaleOfferReason => {
        const decision = ask(over);
        if (decision.offer) throw new Error(`expected silence, got an offer of ${decision.displayName}`);
        return decision.reason;
    };

    it('says nothing to a browser already reading the fallback locale', () => {
        for (const language of ['en', 'en-US', 'en-GB']) {
            expect(ask({ browserLanguage: language })).toEqual({ offer: false, reason: 'english-browser' });
        }
    });

    it('says nothing when the browser language is missing or unreadable', () => {
        expect(whyNot({ browserLanguage: '' })).toBe('english-browser');
        expect(whyNot({ browserLanguage: undefined })).toBe('english-browser');
        expect(whyNot({ browserLanguage: '*' })).toBe('english-browser');
    });

    it('says nothing when no registry entry speaks that language', () => {
        // A browser whose language has no pack is silent, whatever else the index
        // contains — and a *language* with no translation pack is not a pack either.
        expect(
            whyNot({
                browserLanguage: 'fr',
                registry: [makeEntry({ name: 'locale-ja', locale: 'ja' }), makeEntry({ name: 'locale-ko', locale: 'ko' })],
            }),
        ).toBe('no-matching-pack');
        expect(
            whyNot({ browserLanguage: 'fr', registry: [makeEntry({ name: 'gopls', locale: undefined, type: 'lsp' })] }),
        ).toBe('no-matching-pack');
    });

    it('says nothing when the registry itself is missing — silence, never an error', () => {
        expect(whyNot({ registry: null })).toBe('no-matching-pack');
        expect(whyNot({ registry: undefined })).toBe('no-matching-pack');
        expect(whyNot({ registry: [] })).toBe('no-matching-pack');
    });

    it('says nothing while any pack is enabled — the language is already chosen', () => {
        expect(ask({ extensions: [makePack({ name: 'locale-ja', locale: 'ja' })] })).toEqual({
            offer: false,
            reason: 'pack-enabled',
        });
        // Even a pack for a different language: two enabled packs is a resolved
        // ordering, not a reason to offer a third.
        expect(whyNot({ extensions: [makePack({ name: 'locale-ko', locale: 'ko' })] })).toBe('pack-enabled');
        // A pack of any type that *declares* the capability counts, exactly as the
        // loader resolves it.
        expect(whyNot({ extensions: [makePack({ name: 'heavy', locale: 'ja', type: 'ui', declare: true })] })).toBe(
            'pack-enabled',
        );
    });

    it('counts an installed-but-disabled pack as no pack enabled, and still does not offer it', () => {
        // Rule 1 is about *enabled* packs, so a disabled one does not reach
        // 'pack-enabled' — it stops at 'installed-entry', because installing it
        // again would 409 and a pack the user switched off was their decision.
        const disabled = makePack({ name: 'locale-ja', locale: 'ja', enabled: false });
        expect(whyNot({ extensions: [disabled] })).toBe('installed-entry');
    });

    it('says nothing once the user has been asked — dismissed, installed or failed', () => {
        for (const outcome of ['dismissed', 'installed', 'failed'] as const) {
            expect(ask({ record: record(outcome) })).toEqual({ offer: false, reason: 'already-asked' });
        }
        // The failure case is the one that matters: a broken registry must not
        // turn into an offer on every boot.
        expect(whyNot({ record: record('failed'), registry: matching })).toBe('already-asked');
    });

    it('does not offer a pack the registry install path could not install', () => {
        // `POST /extensions/install` is git-pinned: an entry with no repo/commit
        // can only fail, so it is not an offer.
        expect(whyNot({ registry: [makeEntry({ name: 'locale-ja', locale: 'ja', repo: '' })] })).toBe(
            'no-matching-pack',
        );
        expect(whyNot({ registry: [makeEntry({ name: 'locale-ja', locale: 'ja', commit: '' })] })).toBe(
            'no-matching-pack',
        );
    });

    it('does not offer an entry the user already has installed', () => {
        // Registry identity is `<author-slug>/<name>`: `Tester/locale-ja` and an
        // installed extension with author "Tester" and name `locale-ja` are the
        // same pack, and installing it again answers 409.
        const installed = makePack({ name: 'locale-ja', author: 'Tester', enabled: false });
        expect(whyNot({ extensions: [installed] })).toBe('installed-entry');
    });
});

describe('entryDisplayName', () => {
    it('uses the registry display name verbatim — core holds no language names', () => {
        expect(entryDisplayName(makeEntry({ name: 'locale-ja', display_name: '日本語' }))).toBe('日本語');
        expect(entryDisplayName(makeEntry({ name: 'locale-ko', display_name: '  한국어  ' }))).toBe('한국어');
        expect(entryDisplayName(makeEntry({ name: 'locale-ko', display_name: '' }))).toBe('locale-ko');
    });
});

describe('the record key', () => {
    it('is the app-settings key the service reads and writes', () => {
        expect(LOCALE_OFFER_SETTING_KEY).toBe('locale-offer');
    });
});
