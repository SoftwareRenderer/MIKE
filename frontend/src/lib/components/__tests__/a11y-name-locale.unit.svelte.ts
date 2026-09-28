// @vitest-environment jsdom
import { describe, it, expect, afterEach } from 'vitest';
import { render, cleanup } from '@testing-library/svelte';
import BaseModal from '$lib/components/BaseModal.svelte';
import ResizeHandle from '$lib/components/ResizeHandle.svelte';
import { i18n } from '$lib/i18n/runtime.svelte';
import { DEFAULT_LOCALE } from '$lib/i18n/en';

// An accessible name is the only copy a screen reader gets, so it moves with the catalog. These
// pin the two shapes that can go stale: a name read at the tag, and a fallback held in a script
// value instead of the markup.
async function flush() {
    for (let i = 0; i < 6; i++) await Promise.resolve();
}

afterEach(() => {
    cleanup();
    i18n.setCatalog(DEFAULT_LOCALE, null);
});

describe('accessible names under a translation pack', () => {
    it('re-names the icon-only close button in both its tooltip and its name', async () => {
        const { container } = render(BaseModal, { title: 'Whatever', onClose: () => {} });
        const close = container.querySelector('.close-btn') as HTMLElement;

        expect(close.getAttribute('aria-label')).toBe('Close');
        // One key feeds both, so a pack cannot translate the name and leave the tooltip English.
        expect(close.getAttribute('title')).toBe('Close');

        i18n.setCatalog('fr', { 'app.close': 'Fermer' });
        await flush();

        expect(close.getAttribute('aria-label')).toBe('Fermer');
        expect(close.getAttribute('title')).toBe('Fermer');
    });

    it('re-resolves a handle name the caller left to the component', async () => {
        const noop = () => {};
        const { container } = render(ResizeHandle, { onDragStart: noop, onDragMove: noop });
        const handle = container.querySelector('[role="separator"]') as HTMLElement;

        expect(handle.getAttribute('aria-label')).toBe('Resize panel');

        i18n.setCatalog('fr', { 'app.resizePanelAria': 'Redimensionner le panneau' });
        await flush();

        expect(handle.getAttribute('aria-label')).toBe('Redimensionner le panneau');
    });

    it('keeps a name the caller passed instead of translating the fallback', async () => {
        const noop = () => {};
        const { container } = render(ResizeHandle, {
            ariaLabel: 'Resize the editor', onDragStart: noop, onDragMove: noop,
        });

        i18n.setCatalog('fr', { 'app.resizePanelAria': 'Redimensionner le panneau' });
        await flush();

        expect((container.querySelector('[role="separator"]') as HTMLElement).getAttribute('aria-label'))
            .toBe('Resize the editor');
    });
});
