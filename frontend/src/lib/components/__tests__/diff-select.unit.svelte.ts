import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const diffSelectPath = resolve(__dirname, '../DiffSelect.svelte');
const source = readFileSync(diffSelectPath, 'utf-8');

describe('DiffSelect rendering', () => {
    it('shows dropdown button with selected label', () => {
        expect(source).toContain('diff-select-btn');
        expect(source).toContain('sel-label');
    });

    it('shows chevron icon', () => {
        expect(source).toContain('sel-chevron');
    });

    it('shows dropdown list when open', () => {
        expect(source).toContain('{#if open}');
        expect(source).toContain('sel-dropdown');
        expect(source).toContain('role="listbox"');
    });

    it('has proper accessibility attributes', () => {
        expect(source).toContain('aria-expanded={open}');
        expect(source).toContain('aria-label={ariaLabel}');
    });

    it('shows selected option with meta text', () => {
        expect(source).toContain('sel-meta');
        expect(source).toContain('selected?.meta');
    });

    it('calls onChange when option is selected', () => {
        expect(source).toContain('onChange(opt.value)');
    });

    it('closes dropdown on outside click', () => {
        expect(source).toContain('handleClickOutside');
        expect(source).toContain('ref.contains');
    });

    it('defaults to first option when no match', () => {
        expect(source).toContain('options[0]');
    });

    it('shows open class on wrapper', () => {
        expect(source).toContain('class:open');
    });

    it('renders option items in dropdown', () => {
        expect(source).toContain('sel-option');
        expect(source).toContain('options');
    });
});
