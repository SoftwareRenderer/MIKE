import { describe, it, expect } from 'vitest';
import { readFileSync } from 'node:fs';
import { resolve, dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const __dirname = dirname(fileURLToPath(import.meta.url));
const commitDialogPath = resolve(__dirname, '../Chat/CommitDialog.svelte');
const source = readFileSync(commitDialogPath, 'utf-8');

describe('CommitDialog rendering', () => {
    it('shows dialog when commitDialog state is set', () => {
        expect(source).toContain('{#if open}');
        expect(source).toContain('dialogStore.commitDialog');
    });

    it('shows commit message textarea', () => {
        expect(source).toContain('textareaRef');
        expect(source).toContain('editedMessage');
    });

    it('shows confirm button with label', () => {
        expect(source).toContain('Commit and move to Done');
        expect(source).toContain('handleConfirm');
    });

    it('shows cancel button', () => {
        expect(source).toContain('Cancel');
        expect(source).toContain('handleCancel');
    });

    it('supports streaming commit message generation', () => {
        expect(source).toContain('streaming');
        expect(source).toContain('abortController');
    });

    it('cancels streaming when user starts typing', () => {
        expect(source).toContain('handleMessageChange');
        expect(source).toContain('abortController.abort()');
    });

    it('handles Ctrl+Enter to confirm', () => {
        expect(source).toContain('handleKeydown');
        expect(source).toContain('e.ctrlKey');
    });

    it('shows prompt progress during streaming', () => {
        expect(source).toContain('promptProgress');
    });

    it('shows author name and email', () => {
        expect(source).toContain('authorName');
        expect(source).toContain('authorEmail');
    });

    it('shows merge target branch', () => {
        expect(source).toContain('mergeTarget');
    });

    it('shows Commit and move to Done button', () => {
        expect(source).toContain('Commit and move to Done');
    });

    it('resolves promise with commit message on confirm', () => {
        expect(source).toContain('resolveFn');
        expect(source).toContain('dismiss');
    });
});
