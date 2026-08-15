import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const virtualListPath = resolve(__dirname, '../VirtualList.svelte');
const source = readFileSync(virtualListPath, 'utf-8');

describe('VirtualList rendering (fixed-height mode)', () => {
    it('supports fixed-height items via itemHeight', () => {
        expect(source).toContain('itemHeight = 28');
    });

    it('windowing only activates at minThreshold', () => {
        expect(source).toContain('minThreshold');
        expect(source).toMatch(/items\.length\s*>=\s*minThreshold/);
    });

    it('supports the overscan buffer', () => {
        expect(source).toContain('buffer');
        expect(source).toMatch(/-\s*buffer/);
        expect(source).toMatch(/\+\s*buffer/);
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
        expect(source).toContain('@render children');
    });

    it('exposes scroll helpers for parent access', () => {
        expect(source).toContain('scrollToBottom');
        expect(source).toContain('scrollToItem');
        expect(source).toContain('isNearBottom');
    });

    // Regression guards: the variable-height virtualizer was removed (its
    // measuredHeights cache was a source of unbounded growth). It must not
    // creep back in — variable-height content belongs in
    // content-visibility-based rendering (see ChatEntries.svelte).
    it('does not import the useVirtualScroll composable', () => {
        expect(source).not.toContain('useVirtualScroll');
        expect(source).not.toContain('use-virtual-scroll');
    });

    it('does not use ResizeObserver/MutationObserver item measurement', () => {
        expect(source).not.toContain('ResizeObserver');
        expect(source).not.toContain('MutationObserver');
    });
});
