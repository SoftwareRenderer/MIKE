import { describe, it, expect } from 'vitest';
import { countByCategory, filterByCategory, visibleCategories } from '../extension-categories';
import type { ExtensionType } from '../../types';

function item(name: string, type: ExtensionType) {
    return { name, type };
}

const LIST = [
    item('gopls', 'lsp'),
    item('theme', 'css'),
    item('pi', 'agent'),
    item('gopls-extra', 'lsp'),
];

describe('the category quick-search resolver', () => {
    it('hands back the list as it stands when nothing is selected', () => {
        expect(filterByCategory(LIST, new Set<ExtensionType>())).toEqual(LIST);
    });

    it('keeps only the selected types, in the order the list already had', () => {
        const picked = new Set<ExtensionType>(['lsp', 'css']);
        expect(filterByCategory(LIST, picked).map(i => i.name)).toEqual(['gopls', 'theme', 'gopls-extra']);
    });

    it('counts over the un-narrowed list, so a selection leaves the other numbers alone', () => {
        expect([...countByCategory(LIST).entries()]).toEqual([['lsp', 2], ['css', 1], ['agent', 1]]);
        // Narrowing then re-counting is what would collapse the badges to the selection, so the resolver
        // is only ever meant to count the list before the narrowing.
        const narrowed = filterByCategory(LIST, new Set<ExtensionType>(['lsp']));
        expect(countByCategory(narrowed).get('css')).toBeUndefined();
    });

    it('offers categories in the fixed order, never in the order items arrived', () => {
        expect(visibleCategories(countByCategory(LIST))).toEqual(['lsp', 'agent', 'css']);
    });

    it('offers no empty category, so the row covers exactly the cards the list shows', () => {
        expect(visibleCategories(countByCategory([]))).toEqual([]);
        expect(visibleCategories(countByCategory(LIST))).not.toContain('ui');
    });

    it('offers a type the fixed list does not name, after the known ones', () => {
        // A local manifest whose capabilities map to no type is scanned as type "" — the row must still
        // be able to offer it, or its card shows with no way to reach it.
        const counts = countByCategory([item('mystery', 'theme' as ExtensionType), item('gopls', 'lsp')]);
        expect(visibleCategories(counts)).toEqual(['lsp', 'theme']);
    });
});
