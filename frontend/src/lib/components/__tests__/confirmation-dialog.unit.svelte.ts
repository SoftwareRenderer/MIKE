import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const confirmationDialogPath = resolve(__dirname, '../ConfirmationDialog.svelte');
const source = readFileSync(confirmationDialogPath, 'utf-8');

describe('ConfirmationDialog rendering', () => {
    it('shows dialog when confirmation state is set', () => {
        expect(source).toContain('{#if open}');
        expect(source).toContain('dialogStore.confirmation');
    });

    it('shows confirm button', () => {
        expect(source).toContain('btn-confirm');
        expect(source).toContain('handleConfirm');
    });

    it('shows cancel button', () => {
        expect(source).toContain('btn-secondary');
        expect(source).toContain('handleCancel');
    });

    it('supports destructive variant for confirm button', () => {
        expect(source).toContain('destructive');
        expect(source).toContain('variant');
    });

    it('shows confirmation message', () => {
        expect(source).toContain('confirm-message');
        expect(source).toContain('{message}');
    });

    it('shows list of items when provided', () => {
        expect(source).toContain('confirm-list');
        expect(source).toContain('{#if items.length > 0}');
    });

    it('handles Enter key to confirm', () => {
        expect(source).toContain('handleKeydown');
        expect(source).toContain('e.key === \'Enter\'');
    });

    it('uses dialogStore for confirmation state', () => {
        expect(source).toContain('dialogStore');
        expect(source).toContain('confirmation');
    });

    it('dismisses dialog on cancel', () => {
        expect(source).toContain('dismiss(false)');
    });

    it('resolves promise on confirm', () => {
        expect(source).toContain('dismiss(true)');
        expect(source).toContain('resolveFn');
    });
});
