import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const toggleSwitchPath = resolve(__dirname, '../ToggleSwitch.svelte');
const source = readFileSync(toggleSwitchPath, 'utf-8');

describe('ToggleSwitch rendering', () => {
    it('renders as a button with role switch', () => {
        expect(source).toContain('type="button"');
        expect(source).toContain('role="switch"');
    });

    it('has proper accessibility attributes', () => {
        expect(source).toContain('aria-checked={checked}');
        expect(source).toContain('aria-label={ariaLabel}');
        expect(source).toContain('aria-describedby={ariaDescribedby}');
    });

    it('toggles checked state on click', () => {
        expect(source).toContain('checked = !checked');
        expect(source).toContain('onclick');
    });

    it('stops click propagation', () => {
        expect(source).toContain('e.stopPropagation()');
    });

    it('prevents toggle when disabled', () => {
        expect(source).toContain('!disabled');
        expect(source).toContain('disabled={disabled}');
    });

    it('applies active class when checked', () => {
        expect(source).toContain('class:active={checked}');
        expect(source).toContain('.toggle-switch.active');
    });

    it('shows toggle knob', () => {
        expect(source).toContain('toggle-knob');
    });

    it('animates knob position when active', () => {
        expect(source).toContain('transform: translateX(18px)');
    });

    it('has focus-visible outline', () => {
        expect(source).toContain('focus-visible');
        expect(source).toContain('outline');
    });

    it('uses bindable checked prop for two-way binding', () => {
        expect(source).toContain('$bindable');
    });
});
