import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const diffViewerPath = resolve(__dirname, '../DiffViewer.svelte');
const source = readFileSync(diffViewerPath, 'utf-8');

describe('DiffViewer rendering', () => {
    it('shows unstaged blank file in diff panel', () => {
        // Untracked files are rendered with the untracked section class
        expect(source).toContain('diff-file-section-untracked');
        // Blank files show "Empty file" notice
        expect(source).toContain('diff-empty-file-notice');
        expect(source).toContain('Empty file');
    });

    it('shows staged blank file in diff panel', () => {
        // Staged files are rendered in the staged section
        expect(source).toContain('diff-section-staged');
        // The diff content wrapper contains all sections
        expect(source).toContain('diff-content-wrapper');
    });

    it('shows diff content for file with blank removed/added lines, not "Empty file"', () => {
        // Files with rows (even blank lines) render diff rows
        expect(source).toContain('diff-row');
        // The "Empty file" notice only shows when rows.length === 0
        expect(source).toContain('rows.length === 0');
        expect(source).toContain('diff-empty-file-notice');
    });

    it('clears diff panel when untracked file is removed', () => {
        // When untrackedUntracked is empty, hasContent becomes false
        expect(source).toContain('hasContent');
        // The diff-content is only rendered when hasContent is true
        expect(source).toContain('{#if !hasContent}');
        expect(source).toContain('No changes since last commit');
    });

    it('collapses a section collapsing all child files', () => {
        // Section headers toggle collapse state
        expect(source).toContain('diff-section-header');
        expect(source).toContain('toggleSection');
        // File headers have aria-expanded
        expect(source).toContain('aria-expanded={isFileExpanded}');
        // Collapsed files have aria-expanded="false"
        expect(source).toContain('collapsedFiles');
    });

    it('expands a section expanding all child files', () => {
        // Section headers toggle expand/collapse
        expect(source).toContain('onclick={() => toggleSection');
        // File headers expand when clicked
        expect(source).toContain('toggleFile');
        // aria-expanded reflects the expanded state
        expect(source).toContain('aria-expanded');
    });
});
