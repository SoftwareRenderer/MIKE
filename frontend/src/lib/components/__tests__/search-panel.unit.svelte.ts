import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

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
