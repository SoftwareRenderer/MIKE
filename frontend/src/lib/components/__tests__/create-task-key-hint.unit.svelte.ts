import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const columnPath = resolve(__dirname, '../Column.svelte');
const source = readFileSync(columnPath, 'utf-8');
const buttonLine = source.split('\n').find(l => l.includes('add-task-btn') && l.includes('onclick')) || '';

describe('Create Task button key hint', () => {
    it('renders the shortcut as a styled <kbd> keycap, not literal brackets', () => {
        expect(buttonLine).toContain('<kbd class="key-hint">');
        expect(buttonLine).not.toContain('[');
    });

    it('hides the hint when the shortcut is disabled or the user hid hints', () => {
        // The <kbd> must be gated on both the label and the hide-hints preference
        expect(/\{#if\s+createTaskShortcutLabel\s*&&\s*!hintsHidden\}\s*<kbd class="key-hint">/.test(source)).toBe(true);
        expect(source).toContain('shortcutHintsHidden');
    });

    it('includes the shortcut in the button tooltip', () => {
        expect(buttonLine).toContain('Create a new task (');
        expect(buttonLine).toContain('createTaskShortcutLabel');
    });
});
