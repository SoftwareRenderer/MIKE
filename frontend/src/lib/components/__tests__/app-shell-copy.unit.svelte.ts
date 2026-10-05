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
        beforeEach(() => {
            // The form's first body is decided by what this browser can do, so the copy test names that
            // surface rather than inheriting whatever the environment happens to provide.
            Object.defineProperty(window, 'isSecureContext', { value: true, configurable: true });
            Object.defineProperty(window, 'PublicKeyCredential', { value: function () { }, configurable: true });
            Object.defineProperty(navigator, 'credentials', {
                value: { create: async () => null, get: async () => null },
                configurable: true,
            });
        });

        it('shows the English every login locator is anchored on', () => {
            const { container } = render(LoginModal, { onClose: () => { } });
            expect(container.querySelector('h2')?.textContent).toBe('Sign in');
            const button = container.querySelector('.login-btn');
            expect(button?.textContent).toBe('Sign in with a passkey');
            expect(button?.hasAttribute('disabled')).toBe(false);
            // The way out for a browser with no passkey and a code in hand.
            expect(container.querySelector('.login-link')?.textContent).toBe('I have a pairing code');
        });

        it('asks a fresh install for the pairing code, never for a passkey it does not have', () => {
            const { container } = render(LoginModal, { hasPasskey: false, onClose: () => { } });
            expect(container.querySelector('h2')?.textContent).toBe('Pair this device');
            expect(container.querySelector('.login-btn')?.textContent).toBe('Create a passkey');
            // Nothing to spend a ceremony on yet.
            expect(container.querySelector('.login-btn')?.hasAttribute('disabled')).toBe(true);
        });

        it('labels the code field for the reader who cannot see the label', () => {
            const { container } = render(LoginModal, { hasPasskey: false, onClose: () => { } });
            const code = container.querySelector('input[type="text"]');
            expect(code?.getAttribute('aria-label')).toBe('Pairing code');
            expect(code?.getAttribute('placeholder')).toBe('XXXX-XXXX');
        });

        it('lists the three places a pairing code comes from', () => {
            const { container } = render(LoginModal, { hasPasskey: false, onClose: () => { } });

            const items = [...container.querySelectorAll('.login-list li')].map((li) => li.textContent);
            expect(items).toEqual([
                t('app.login.codeSourceAdmin'),
                t('app.login.codeSourceBoot'),
                t('app.login.codeSourceSettings'),
            ]);
            // The stem only introduces the list: the routes are said once, in the list, and the list is part
            // of the field's group rather than a paragraph of its own.
            expect(container.querySelector('.login-field .login-copy')?.textContent).toBe(t('app.login.codeHelp'));
        });

        // The browser that has to treat this address as secure is the one being paired, and its rule is a
        // prerequisite of the ceremony rather than something this page can measure, so the certificate is
        // offered wherever there is one — not only where this connection already looks wrong.
        describe('the certificate a pairing needs the device to trust', () => {
            it('is offered on the pairing body over a connection this browser already trusts', () => {
                const { container } = render(LoginModal, { hasPasskey: false, caDownload: true, onClose: () => { } });

                expect(container.textContent).toContain(t('app.login.caPrerequisite'));
                expect(container.textContent).toContain('certificate authority');
                const link = container.querySelector('a[download]');
                expect(link?.getAttribute('href')).toBe('/api/tls/ca');
                expect(link?.getAttribute('download')).toBe('mike-ca.crt');
                expect(container.textContent).toContain('install it as a trusted root certificate');
                // Nothing here says this connection is the problem, because it is not.
                expect(container.textContent).not.toContain('does not treat this connection as secure');
                // And the ceremony is still the point of the screen: the offer must not eat the button.
                expect(container.querySelector('.login-btn')?.textContent).toBe('Create a passkey');
            });

            it('is not offered where the server has no certificate to hand out', () => {
                const { container } = render(LoginModal, { hasPasskey: false, caDownload: false, onClose: () => { } });

                expect(container.querySelector('a[download]')).toBeNull();
                expect(container.textContent).not.toContain('certificate');
            });
        });

        // A phone on the LAN address reaches this body: WebAuthn is a secure-context API, so the form
        // has no prompt to run and the explanation is the whole screen (docs/tls.md).
        describe('a connection the browser will not run a prompt on', () => {
            beforeEach(() => {
                Object.defineProperty(window, 'isSecureContext', { value: false, configurable: true });
            });

            it('says the browser does not trust this network, and hands over the certificate it has', () => {
                const { container } = render(LoginModal, { hasPasskey: false, caDownload: true, onClose: () => { } });

                expect(container.querySelector('.login-copy')?.textContent)
                    .toContain('does not treat this connection as secure');
                const link = container.querySelector('a[download]');
                expect(link?.getAttribute('href')).toBe('/api/tls/ca');
                expect(link?.getAttribute('download')).toBe('mike-ca.crt');
                expect(link?.textContent).toBe('Download the certificate authority (mike-ca.crt)');
                expect(container.textContent).toContain('install it as a trusted root certificate');
                // The steps already end at the https address, so the same instruction is not printed twice.
                expect(container.textContent?.match(/reload/g)).toHaveLength(1);
            });

            it('offers no link to a certificate this server does not have', () => {
                const { container } = render(LoginModal, { hasPasskey: false, caDownload: false, onClose: () => { } });

                expect(container.querySelector('a[download]')).toBeNull();
                expect(container.textContent).not.toContain('trusted root certificate');
                // The way out that needs no certificate is still on the screen.
                expect(container.textContent).toContain('http://localhost');
            });

            // A pairing link is the only way a second device gets a code nobody read out to it, and it opens
            // the code body whatever the connection turns out to be — so the certificate belongs on that
            // body too, not only on the explanation the first paint shows.
            describe('the code body a pairing link opens', () => {
                // The link is a fragment on this document, and the modal strips it as it reads it.
                beforeEach(() => history.replaceState(null, '', window.location.pathname));
                afterEach(() => history.replaceState(null, '', window.location.pathname));

                it('keeps the code and hands over the certificate', () => {
                    history.replaceState(null, '', '#pair=ABCD2345');
                    const { container } = render(LoginModal, { hasPasskey: true, caDownload: true, onClose: () => { } });

                    // The code field is still the body: spending the code is what they arrived to do.
                    expect(container.querySelector('h2')?.textContent).toBe('Pair this device');
                    expect(container.querySelector<HTMLInputElement>('input[type="text"]')?.value).toBe('ABCD-2345');
                    expect(container.textContent).toContain('does not treat this connection as secure');
                    const link = container.querySelector('a[download]');
                    expect(link?.getAttribute('href')).toBe('/api/tls/ca');
                    expect(link?.getAttribute('download')).toBe('mike-ca.crt');
                    expect(container.textContent?.match(/reload/g)).toHaveLength(1);
                });

                it('names the localhost way out when there is no certificate to hand over', () => {
                    history.replaceState(null, '', '#pair=ABCD2345');
                    const { container } = render(LoginModal, { hasPasskey: true, caDownload: false, onClose: () => { } });

                    expect(container.querySelector('a[download]')).toBeNull();
                    expect(container.querySelector<HTMLInputElement>('input[type="text"]')?.value).toBe('ABCD-2345');
                    expect(container.textContent).toContain('http://localhost');
                });
            });
        });

        // `no-api` is a different fault with a different fix, so the certificate is not on this screen.
        it('offers no certificate to a browser that has no passkey API on a connection it trusts', () => {
            Object.defineProperty(window, 'PublicKeyCredential', { value: undefined, configurable: true });
            const { container } = render(LoginModal, { hasPasskey: true, caDownload: true, onClose: () => { } });

            expect(container.textContent).toContain('cannot create a passkey');
            expect(container.querySelector('a[download]')).toBeNull();
        });

        it('names the three things the form can report', () => {
            expect(t('app.login.failed')).toBe('Login failed');
            // A dismissed prompt and a refused passkey are different sentences in different colours.
            expect(t('app.login.cancelled')).toBe('The passkey prompt was dismissed');
            expect(t('app.login.waiting')).toBe('Waiting for your device...');
        });
    });

    describe('DialogStore defaults', () => {
        it('confirms in English when the caller names no buttons', async () => {
            const store = new DialogStore();
            void store.showConfirm({ title: 'T', message: 'M' });
            expect(store.confirmationSlot.pending?.confirmText).toBe('Confirm');
            expect(store.confirmationSlot.pending?.cancelText).toBe('Cancel');
            store.confirmationSlot.pending?._resolve?.(true);
        });

        it('labels the two specialised dialogs with their own verb', () => {
            const store = new DialogStore();
            void store.showCompressContextDialog({});
            expect(store.compressContextDialogSlot.pending?.confirmText).toBe('Compress');
            expect(store.compressContextDialogSlot.pending?.cancelText).toBe('Cancel');

            void store.showCommitDialog({ message: 'm' });
            expect(store.commitDialogSlot.pending?.confirmText).toBe('Commit and move to Done');
            expect(store.commitDialogSlot.pending?.cancelText).toBe('Cancel');
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
