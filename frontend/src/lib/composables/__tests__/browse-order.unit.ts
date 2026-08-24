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
});
