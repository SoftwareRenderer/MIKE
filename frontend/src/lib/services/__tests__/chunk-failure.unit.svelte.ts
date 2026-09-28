import { describe, it, expect, beforeEach, vi } from 'vitest';

// A document older than the server imports chunk URLs that no longer exist, and only a notice can
// tell the user — the panel they clicked on is simply not there.

vi.mock('$lib/stores', async () => {
    const { DialogStore } = await import('$lib/stores/dialogs.svelte');
    return { dialogStore: new DialogStore(), uiStore: {}, appStore: {} };
});

import { isChunkLoadFailure } from '$lib/services/chunk-failure';

const CHUNK_ERRORS = [
    'Failed to fetch dynamically imported module: https://host/assets/TerminalPanel-abc123.js',
    'Unable to preload CSS for /assets/DiffViewer-def456.css',
    'Importing a module script failed.',
];

describe('a lazy chunk the browser could not fetch', () => {
    beforeEach(() => {
        vi.spyOn(console, 'error').mockImplementation(() => {});
    });

    it.each(CHUNK_ERRORS)('recognises %s', (message) => {
        expect(isChunkLoadFailure(new Error(message))).toBe(true);
    });

    it('leaves a failed data request alone — reloading is not the fix for it', () => {
        expect(isChunkLoadFailure(new TypeError('Failed to fetch'))).toBe(false);
        expect(isChunkLoadFailure(new Error('Failed to send message'))).toBe(false);
        expect(isChunkLoadFailure(undefined)).toBe(false);
    });

    it('tells the user to reload, once, however many clicks follow', async () => {
        // The notice is once per page life, so this case opens its own: a fresh module graph hands
        // back the latch and the store that latch writes to, from one instantiation.
        vi.resetModules();
        const { reportChunkFailure } = await import('$lib/services/chunk-failure');
        const { dialogStore } = await import('$lib/stores');
        const failure = new Error(CHUNK_ERRORS[0]);

        reportChunkFailure(failure);
        const first = dialogStore.toast;
        expect(first?.type).toBe('error');
        expect(first?.message.toLowerCase()).toContain('reload');

        reportChunkFailure(failure);
        expect(dialogStore.toast).toBe(first);
    });
});
