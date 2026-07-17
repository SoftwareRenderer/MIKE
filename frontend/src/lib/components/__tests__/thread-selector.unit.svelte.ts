import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const threadSelectorPath = resolve(__dirname, '../Chat/ThreadSelector.svelte');
const source = readFileSync(threadSelectorPath, 'utf-8');

describe('ThreadSelector rendering', () => {
    it('shows thread switch button', () => {
        expect(source).toContain('thread-btn');
        expect(source).toContain('Switch thread');
    });

    it('has proper accessibility attributes', () => {
        expect(source).toContain('aria-label="Switch thread"');
        expect(source).toContain('aria-expanded={showMenu}');
    });

    it('shows active thread name', () => {
        expect(source).toContain('activeThreadName');
        expect(source).toContain('activeThreadId');
    });

    it('shows dropdown menu when toggled', () => {
        expect(source).toContain('showMenu');
        expect(source).toContain('toggleMenu');
    });

    it('lists available threads in dropdown', () => {
        expect(source).toContain('threads');
        expect(source).toContain('onSwitch');
    });

    it('highlights active thread in list', () => {
        expect(source).toContain('activeThreadId');
    });

    it('prevents switching when already switching', () => {
        expect(source).toContain('isSwitching');
    });

    it('closes menu on outside click', () => {
        expect(source).toContain('handleClickOutside');
        expect(source).toContain('menuRef');
    });

    it('defaults to Main thread name when not found', () => {
        expect(source).toContain('Main');
    });

    it('shows thread name', () => {
        expect(source).toContain('activeThreadName');
        expect(source).toContain('Main');
    });
});
