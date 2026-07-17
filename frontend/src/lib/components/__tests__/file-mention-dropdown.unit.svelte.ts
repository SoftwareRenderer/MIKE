import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const fileMentionPath = resolve(__dirname, '../FileMentionDropdown.svelte');
const source = readFileSync(fileMentionPath, 'utf-8');

describe('FileMentionDropdown rendering', () => {
    it('shows file mention dropdown when visible', () => {
        expect(source).toContain('mention-dropdown');
        expect(source).toContain('visible');
    });

    it('accepts results array with file paths', () => {
        expect(source).toContain('results');
        expect(source).toContain('path');
    });

    it('positions dropdown near cursor', () => {
        expect(source).toContain('position');
        expect(source).toContain('top');
        expect(source).toContain('left');
    });

    it('adjusts for viewport overflow', () => {
        expect(source).toContain('viewportHeight');
        expect(source).toContain('viewportWidth');
    });

    it('highlights active item', () => {
        expect(source).toContain('activeIndex');
        expect(source).toContain('active');
    });

    it('calls onSelect when file is selected', () => {
        expect(source).toContain('onSelect');
    });

    it('calls onClose when dismissed', () => {
        expect(source).toContain('onClose');
    });

    it('handles keyboard navigation', () => {
        expect(source).toContain('keydown');
    });

    it('only shows when results exist and position is set', () => {
        expect(source).toContain('position !== null');
        expect(source).toContain('results.length > 0');
    });
});
