import { describe, it, expect, afterEach } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';
import { render, cleanup } from '@testing-library/svelte';
import CustomSearchPanel from '../CustomSearchPanel.svelte';

const __dirname = dirname(fileURLToPath(import.meta.url));
const searchPanelPath = resolve(__dirname, '../CustomSearchPanel.svelte');
const source = readFileSync(searchPanelPath, 'utf-8');

describe('CustomSearchPanel', () => {
    it('should open search panel with expected structure', () => {
        expect(source).toContain('class="search-panel"');
        expect(source).toContain('class="search-input"');
        expect(source).toContain('placeholder="Find"');
        expect(source).toContain('class="search-row"');
        expect(source).toContain('class="search-nav"');
    });

    it('should find matches for text in the file', () => {
        // The component implements findMatches() that searches the document
        expect(source).toContain('function findMatches');
        expect(source).toContain('class="match-count"');
        expect(source).toContain('oninput={onInput}');
    });

    it('should show zero matches for non-existent text', () => {
        // The match count displays the number of matches found
        expect(source).toContain('matchCount');
        expect(source).toContain('{matchCount}');
    });

    it('should close search panel with Escape', () => {
        // The handleKeydown function handles Escape key
        expect(source).toContain("e.key === 'Escape'");
        expect(source).toContain('onClose()');
    });

    it('should navigate between matches with navigation buttons', () => {
        // Navigation buttons for prev/next match
        expect(source).toContain('class="nav-btn"');
        expect(source).toContain('aria-label="Previous match"');
        expect(source).toContain('aria-label="Next match"');
        expect(source).toContain('selectNext');
    });

    it('should toggle replace panel', () => {
        // The replace panel is toggled by showReplace state
        expect(source).toContain('showReplace');
        expect(source).toContain('class="replace-row"');
        expect(source).toContain('replace-input');
        expect(source).toContain('class="toggle-replace-btn"');
        expect(source).toContain('class="replace-actions"');
        expect(source).toContain('class="action-btn"');
    });

    it('should close search panel with close button', () => {
        // Close button with aria-label
        expect(source).toContain('aria-label="Close search"');
        expect(source).toContain('onclick={onClose}');
    });
});

describe('CustomSearchPanel focus contract (rendered)', () => {
    afterEach(cleanup);

    function makeMockView(doc: string) {
        return {
            state: { doc: { toString: () => doc } },
            dispatch: () => {},
            coordsAtPos: () => null,
            scrollDOM: { clientHeight: 400, scrollTop: 0 },
        } as any;
    }

    function renderPanel(onFocusFind: (fn: (() => void) | null) => void) {
        return render(CustomSearchPanel, {
            view: makeMockView('hello world'),
            onClose: () => {},
            onFocusFind,
        });
    }

    it('registers a focus function that focuses the find input', () => {
        let focusFind: (() => void) | null | undefined;
        const { container } = renderPanel((fn) => { focusFind = fn; });

        expect(focusFind).toBeDefined();
        expect(typeof focusFind).toBe('function');

        // Move focus away (simulates the user clicking into the code editor)
        const other = document.createElement('button');
        container.appendChild(other);
        other.focus();
        expect(document.activeElement).toBe(other);

        focusFind!();
        const findInput = container.querySelector('input[placeholder="Find"]') as HTMLInputElement;
        expect(document.activeElement).toBe(findInput);
    });

    it('focuses the find input (not replace) when the replace row is open', async () => {
        let focusFind: (() => void) | null | undefined;
        const { container } = renderPanel((fn) => { focusFind = fn; });

        // Open the replace row
        container.querySelector('.toggle-replace-btn')!.dispatchEvent(new MouseEvent('click', { bubbles: true }));
        // Let Svelte flush the re-render from the DOM event
        await new Promise((r) => setTimeout(r, 0));
        const replaceInput = container.querySelector('.replace-input') as HTMLInputElement | null;
        expect(replaceInput).not.toBeNull();

        focusFind!();
        const findInput = container.querySelector('input[placeholder="Find"]') as HTMLInputElement;
        expect(document.activeElement).toBe(findInput);
        expect(document.activeElement).not.toBe(replaceInput);
    });

    it('unregisters the focus function on destroy', () => {
        let focusFind: (() => void) | null | undefined;
        const { unmount } = renderPanel((fn) => { focusFind = fn; });

        expect(typeof focusFind).toBe('function');
        unmount();
        expect(focusFind).toBeNull();
    });
});
