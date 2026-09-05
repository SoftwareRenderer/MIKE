import { describe, it, expect, afterEach, beforeEach } from 'vitest';
import { render, cleanup } from '@testing-library/svelte';

import LoginModal from '$lib/components/LoginModal.svelte';
import { DialogStore } from '$lib/stores/dialogs.svelte';
import { t, resetToEnglish } from '$lib/i18n';

/**
 * The app shell's copy, pinned at the two places it can actually break: what the
 * browser renders, and what the catalog resolves. The e2e suite is the real gate for
 * most of these (`login.spec.ts` asserts `h2` is exactly "Sign in"), but it is not the
 * loop anyone runs per change — and every one of these strings is now a catalog
 * lookup, so an edit to `en.ts` lands here first.
 */
describe('app shell copy', () => {
    beforeEach(() => resetToEnglish());
    afterEach(cleanup);

    describe('LoginModal (renders before config and extensions load)', () => {
        it('shows the English every login locator is anchored on', () => {
            const { container } = render(LoginModal, { onClose: () => { } });
            expect(container.querySelector('h2')?.textContent).toBe('Sign in');
            const button = container.querySelector('.login-btn');
            expect(button?.textContent).toBe('Sign in');
            expect(button?.hasAttribute('disabled')).toBe(true);
        });

        it('leaves the labelling attributes literal (i18n/README.md convention)', () => {
            const { container } = render(LoginModal, { onClose: () => { } });
            const password = container.querySelector('input[type="password"]');
            expect(password?.getAttribute('placeholder')).toBe('Password');
            expect(password?.getAttribute('aria-label')).toBe('Password');
        });

        it('names the two failures the form can report', () => {
            expect(t('app.login.failed')).toBe('Login failed');
            expect(t('app.login.networkError')).toBe('Network error');
            expect(t('app.login.signingIn')).toBe('Signing in...');
        });
    });

    describe('DialogStore defaults', () => {
        it('confirms in English when the caller names no buttons', async () => {
            const store = new DialogStore();
            void store.showConfirm({ title: 'T', message: 'M' });
            expect(store.confirmation?.confirmText).toBe('Confirm');
            expect(store.confirmation?.cancelText).toBe('Cancel');
            store.confirmation?._resolve?.(true);
        });

        it('labels the two specialised dialogs with their own verb', () => {
            const store = new DialogStore();
            void store.showCompressContextDialog({});
            expect(store.compressContextDialog?.confirmText).toBe('Compress');
            expect(store.compressContextDialog?.cancelText).toBe('Cancel');

            void store.showCommitDialog({ message: 'm' });
            expect(store.commitDialog?.confirmText).toBe('Commit and move to Done');
            expect(store.commitDialog?.cancelText).toBe('Cancel');
        });
    });

    describe('interpolated messages', () => {
        it('counts review tasks through plural rules, not `n === 1`', () => {
            expect(t('app.title.review', { name: 'MIKE', count: 1 })).toBe('🟡 MIKE - 1 task for review');
            expect(t('app.title.review', { name: 'MIKE', count: 0 })).toBe('🟡 MIKE - 0 tasks for review');
            expect(t('app.title.review', { name: 'MIKE', count: 12 })).toBe('🟡 MIKE - 12 tasks for review');
        });

        it('keeps the reconnect banner spacing the CSS lays out', () => {
            // "Reconnecting ..." is the whole line with no countdown scheduled;
            // the countdown variant replaces the sentence, not a fragment of it.
            expect(t('app.reconnect.status')).toBe('Reconnecting ...');
            expect(t('app.reconnect.statusCountdown', { seconds: 7 })).toBe('Reconnecting in 7s ...');
            expect(t('app.reconnect.dataFrom', { time: '5 minutes ago' })).toBe('Data from 5 minutes ago');
        });

        it('keeps the remove-project warning readable — name quoted, paragraphs intact', () => {
            const message = t('app.project.removeMessage', { name: 'my repo' });
            expect(message).toBe(
                'Remove "my repo" from the board?\n\n' +
                'This will delete all tasks, messages, and their worktree directories.\n' +
                'The project folder itself will NOT be deleted.',
            );
        });

        it('wraps the merge sentence around the branch badge without gluing the halves', () => {
            expect(t('app.task.autoMergeInto.before')).toBe('Merge changes into');
            expect(t('app.task.autoMergeInto.after')).toBe('when complete');
        });
    });
});
