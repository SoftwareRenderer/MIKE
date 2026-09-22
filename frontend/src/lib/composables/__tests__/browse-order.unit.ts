import { describe, it, expect } from 'vitest';
import { orderBrowseEntries } from '../use-browse-order';
import type { RegistryEntry } from '../../types';

function entry(name: string, author = 'author', extra: Partial<RegistryEntry> = {}): RegistryEntry {
    return {
        name,
        author,
        version: '1.0.0',
        type: 'ui',
        display_name: name,
        description: `description of ${name}`,
        ...extra,
    };
}

describe('orderBrowseEntries', () => {
    it('puts unpublished local entries first, then registry entries in original order', () => {
        const local = [entry('local-a', 'l'), entry('local-b', 'l')];
        const registry = [entry('reg-a'), entry('reg-b')];
        const result = orderBrowseEntries(local, registry, () => false, '');
        expect(result.map(e => e.name)).toEqual(['local-a', 'local-b', 'reg-a', 'reg-b']);
    });

    it('pins featured (not installed) registry entries above the remaining registry entries', () => {
        const registry = [entry('reg-a'), entry('reg-b', 'a', { featured: true }), entry('reg-c')];
        const result = orderBrowseEntries([], registry, () => false, '');
        expect(result.map(e => e.name)).toEqual(['reg-b', 'reg-a', 'reg-c']);
    });

    it('keeps local entries above featured registry entries', () => {
        const local = [entry('local-a', 'l')];
        const registry = [entry('reg-a'), entry('reg-b', 'a', { featured: true })];
        const result = orderBrowseEntries(local, registry, () => false, '');
        expect(result.map(e => e.name)).toEqual(['local-a', 'reg-b', 'reg-a']);
    });

    it('does not pin an installed featured entry — it keeps its normal position', () => {
        const registry = [
            entry('reg-a'),
            entry('reg-b', 'a', { featured: true }),
            entry('reg-c'),
        ];
        const result = orderBrowseEntries([], registry, e => e.name === 'reg-b', '');
        expect(result.map(e => e.name)).toEqual(['reg-a', 'reg-b', 'reg-c']);
    });

    it('keeps relative order of multiple featured entries', () => {
        const registry = [
            entry('reg-a'),
            entry('reg-b', 'a', { featured: true }),
            entry('reg-c', 'c', { featured: true }),
            entry('reg-d'),
        ];
        const result = orderBrowseEntries([], registry, () => false, '');
        expect(result.map(e => e.name)).toEqual(['reg-b', 'reg-c', 'reg-a', 'reg-d']);
    });

    it('treats a falsy/absent featured flag as not featured', () => {
        const registry = [
            entry('reg-a'),
            entry('reg-b', 'a', { featured: false }),
            entry('reg-c'),
        ];
        const result = orderBrowseEntries([], registry, () => false, '');
        expect(result.map(e => e.name)).toEqual(['reg-a', 'reg-b', 'reg-c']);
    });

    it('filters by search query (name, display_name or description) keeping featured first', () => {
        const local = [entry('local-a', 'l')];
        const registry = [
            entry('reg-a'),
            entry('reg-b', 'a', { featured: true }),
            entry('reg-c', 'c', { display_name: 'Reg C' }),
        ];
        const result = orderBrowseEntries(local, registry, () => false, 'reg-b');
        expect(result.map(e => e.name)).toEqual(['reg-b']);
    });

    it('keeps a matching featured entry above matching non-featured entries while searching', () => {
        const registry = [
            entry('alpha-one'),
            entry('alpha-two', 'a', { featured: true }),
            entry('alpha-three'),
        ];
        const result = orderBrowseEntries([], registry, () => false, 'alpha');
        expect(result.map(e => e.name)).toEqual(['alpha-two', 'alpha-one', 'alpha-three']);
    });

    it('search matches description and filters out non-matching entries', () => {
        const registry = [entry('reg-a'), entry('reg-b', 'a', { description: 'only this one mentions zebra' })];
        const result = orderBrowseEntries([], registry, () => false, 'zebra');
        expect(result.map(e => e.name)).toEqual(['reg-b']);
    });

    it('search is case-insensitive', () => {
        const registry = [entry('reg-a'), entry('reg-b')];
        const result = orderBrowseEntries([], registry, () => false, 'REG-A');
        expect(result.map(e => e.name)).toEqual(['reg-a']);
    });

    it('returns an empty list when nothing matches the query', () => {
        const local = [entry('local-a', 'l')];
        const registry = [entry('reg-a')];
        const result = orderBrowseEntries(local, registry, () => false, 'no-such-extension');
        expect(result).toEqual([]);
    });

    describe('the browser-language tier', () => {
        const pack = (name: string, locale: string, extra: Partial<RegistryEntry> = {}) =>
            entry(name, 'author', {
                type: 'i18n',
                capabilities: { i18n: { locale } },
                ...extra,
            });

        it('lists the pack for the browser language after the featured entries', () => {
            const registry = [
                entry('reg-a'),
                entry('feat', 'a', { featured: true }),
                pack('pack-zh', 'zh'),
                entry('reg-b'),
            ];
            const result = orderBrowseEntries([], registry, () => false, '', 'zh-CN');
            expect(result.map(e => e.name)).toEqual(['feat', 'pack-zh', 'reg-a', 'reg-b']);
        });

        it('keeps local entries above the locale pack', () => {
            const registry = [entry('reg-a'), pack('pack-zh', 'zh')];
            const result = orderBrowseEntries([entry('local-a', 'l')], registry, () => false, '', 'zh-CN');
            expect(result.map(e => e.name)).toEqual(['local-a', 'pack-zh', 'reg-a']);
        });

        it('does not promote an installed pack for the browser language', () => {
            const registry = [
                entry('reg-a'),
                pack('pack-zh', 'zh'),
                entry('reg-b'),
                entry('feat', 'f', { featured: true }),
            ];
            const result = orderBrowseEntries([], registry, e => e.name === 'pack-zh', '', 'zh-CN');
            expect(result.map(e => e.name)).toEqual(['feat', 'reg-a', 'pack-zh', 'reg-b']);
        });

        it('lists a featured locale pack once, in the featured tier', () => {
            const registry = [
                entry('reg-a'),
                pack('pack-zh', 'zh', { featured: true }),
                entry('reg-b'),
            ];
            const result = orderBrowseEntries([], registry, () => false, '', 'zh-CN');
            expect(result.map(e => e.name)).toEqual(['pack-zh', 'reg-a', 'reg-b']);
        });

        it('matches on the primary subtag on both sides', () => {
            const registry = [entry('reg-a'), pack('pack-zh', 'zh')];
            const promoted = ['pack-zh', 'reg-a'];
            const unmoved = ['reg-a', 'pack-zh'];
            expect(orderBrowseEntries([], registry, () => false, '', 'zh-CN').map(e => e.name)).toEqual(promoted);
            expect(orderBrowseEntries([], registry, () => false, '', 'zh-Hans-CN').map(e => e.name)).toEqual(promoted);
            expect(orderBrowseEntries([], registry, () => false, '', 'ZH').map(e => e.name)).toEqual(promoted);
            expect(orderBrowseEntries([], registry, () => false, '', 'pt_BR').map(e => e.name)).toEqual(unmoved);
        });

        it('promotes nothing for a fallback-locale browser, a garbage tag, or no tag', () => {
            const registry = [entry('reg-a'), pack('pack-en', 'en'), pack('pack-zh', 'zh')];
            const plain = ['reg-a', 'pack-en', 'pack-zh'];
            for (const tag of ['en-US', 'en', 'en-GB', '*', 'x-pig', '', null, undefined]) {
                expect(orderBrowseEntries([], registry, () => false, '', tag).map(e => e.name)).toEqual(plain);
            }
        });

        it('ignores an entry that declares no i18n locale', () => {
            const registry = [
                entry('reg-a'),
                entry('ui-zh', 'u', { capabilities: { ui: {} } }),
                entry('bare-zh', 'b', { type: 'i18n' }),
                pack('pack-zh', 'zh'),
            ];
            const result = orderBrowseEntries([], registry, () => false, '', 'zh-CN');
            expect(result.map(e => e.name)).toEqual(['pack-zh', 'reg-a', 'ui-zh', 'bare-zh']);
        });

        it('keeps two packs for one language in registry order', () => {
            const registry = [
                entry('reg-a'),
                pack('pack-zh-b', 'zh'),
                pack('pack-zh-a', 'zh'),
                entry('reg-b'),
            ];
            const result = orderBrowseEntries([], registry, () => false, '', 'zh-CN');
            expect(result.map(e => e.name)).toEqual(['pack-zh-b', 'pack-zh-a', 'reg-a', 'reg-b']);
        });

        it('keeps the locale pack above other matches while searching', () => {
            const registry = [
                entry('alpha-one'),
                pack('alpha-pack', 'zh'),
                entry('alpha-two'),
            ];
            const result = orderBrowseEntries([], registry, () => false, 'alpha', 'zh-CN');
            expect(result.map(e => e.name)).toEqual(['alpha-pack', 'alpha-one', 'alpha-two']);
        });

        it('orders as before when the caller passes no browser language at all', () => {
            const registry = [entry('reg-a'), pack('pack-zh', 'zh')];
            expect(orderBrowseEntries([], registry, () => false, '').map(e => e.name)).toEqual(['reg-a', 'pack-zh']);
        });
    });
});
