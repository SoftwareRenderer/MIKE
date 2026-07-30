import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const branchSymbolsPath = resolve(__dirname, '../BranchSymbols.svelte');
const source = readFileSync(branchSymbolsPath, 'utf-8');

describe('BranchSymbols', () => {
    it('accepts symbols and onSymbolClick props', () => {
        expect(source).toContain('symbols:');
        expect(source).toContain('onSymbolClick:');
    });

    it('has tree view toggle with persisted state', () => {
        expect(source).toContain("loadAppSetting('symbols-tree-view'");
        expect(source).toContain("saveAppSetting('symbols-tree-view'");
        expect(source).toContain('toggleTreeView');
    });

    it('has alphabetical sort toggle with persisted state', () => {
        expect(source).toContain("loadAppSetting('symbols-sort-alpha'");
        expect(source).toContain("saveAppSetting('symbols-sort-alpha'");
        expect(source).toContain('toggleSortAlpha');
    });

    it('defaults sortAlpha to true (alphabetical)', () => {
        expect(source).toContain('let sortAlpha = $state(true)');
    });

    it('sorts flatList alphabetically when sortAlpha is true', () => {
        expect(source).toContain('if (sortAlpha)');
        expect(source).toContain('localeCompare');
    });

    it('sorts tree view alphabetically when sortAlpha is true', () => {
        // The tree walk also sorts children
        expect(source).toMatch(/if\s*\(sortAlpha\)\s*\{[^}]*sorted\s*=\s*\[\.\.\.nodes\]\.sort/);
    });

    it('renders sort toggle button in header', () => {
        expect(source).toContain('toggleSortAlpha');
        expect(source).toContain('toggle-btn');
        expect(source).toContain('active');
    });

    it('sort toggle shows active state when alphabetical', () => {
        expect(source).toMatch(/class=.*sortAlpha.*active/);
    });

    it('sort toggle has aria-label for accessibility', () => {
        expect(source).toContain('aria-label');
        expect(source).toContain('alphabetical');
    });

    it('shows 1-2-3 icon when alphabetical sort is active (action to switch)', () => {
        // Icon matches tooltip: when sortAlpha=true, shows position icon = "click to go to position"
        expect(source).toMatch(/{#if\s+sortAlpha}[^]*1-2-3 icon/s);
    });

    it('shows AZ icon when position sort is active (action to switch)', () => {
        expect(source).toMatch(/{:else}[^]*A→Z icon/s);
    });

    it('has CSS for active toggle state', () => {
        expect(source).toContain('.toggle-btn.active');
    });

    it('filters flatList to top-level only (no containerName)', () => {
        expect(source).toContain('!s.containerName');
    });

    it('detects outline mode when all symbols are headings', () => {
        expect(source).toContain("isOutlineMode");
        expect(source).toContain("s.kind === 'heading'");
    });

    it('uses VirtualList for rendering', () => {
        expect(source).toContain('VirtualList');
    });
});
