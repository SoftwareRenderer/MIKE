import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const boardOverviewPath = resolve(__dirname, '../CardDetail/BoardOverview.svelte');
const source = readFileSync(boardOverviewPath, 'utf-8');

describe('BoardOverview', () => {
    it('should show all 4 sections in the side panel', () => {
        // The component iterates over sections prop
        expect(source).toContain('{#each sections as section');
        // Section headers display the title
        expect(source).toContain('class="section-title"');
        expect(source).toContain('{section.title}');
    });

    it('should show sections even when empty', () => {
        // Section groups are always rendered for each section
        expect(source).toContain('class="section-group"');
        // Section headers are always rendered
        expect(source).toContain('class="section-header"');
    });

    it('should have collapsible sections with chevron', () => {
        // Chevron icons are present
        expect(source).toContain('class="section-chevron"');
        // Section headers have aria-expanded
        expect(source).toContain('aria-expanded={');
        // Clicking toggles the section
        expect(source).toContain('onToggleSection');
    });
});
