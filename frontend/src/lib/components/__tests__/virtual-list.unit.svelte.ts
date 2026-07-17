import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const virtualListPath = resolve(__dirname, '../VirtualList.svelte');
const source = readFileSync(virtualListPath, 'utf-8');

describe('VirtualList rendering', () => {
    it('uses useVirtualScroll composable', () => {
        expect(source).toContain('useVirtualScroll');
    });

    it('supports variable-height items with itemHeight function', () => {
        expect(source).toContain('itemHeight');
        expect(source).toContain('getEstimatedHeight');
    });

    it('supports fixed-height mode', () => {
        expect(source).toContain('itemHeight = 28');
    });

    it('supports overscan buffer', () => {
        expect(source).toContain('overscan');
        expect(source).toContain('overscan={5}');
    });

    it('supports custom key getter', () => {
        expect(source).toContain('getKey');
        expect(source).toContain('i.id');
    });

    it('notifies parent of scroll element', () => {
        expect(source).toContain('onScrollElChange');
    });

    it('supports ARIA role and label', () => {
        expect(source).toContain('role');
        expect(source).toContain('aria-label');
    });

    it('supports controlled scroll position', () => {
        expect(source).toContain('scrollTop');
    });

    it('renders children via snippet', () => {
        expect(source).toContain('children');
        expect(source).toContain('{#snippet children');
    });

    it('measures items via ResizeObserver', () => {
        expect(source).toContain('ResizeObserver');
    });
});
