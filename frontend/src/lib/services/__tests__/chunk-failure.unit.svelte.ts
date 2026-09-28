import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';

vi.mock('$lib/stores', async () => {
    const { DialogStore } = await import('$lib/stores/dialogs.svelte');
    return { dialogStore: new DialogStore(), uiStore: {}, appStore: {} };
});

const { mockCheckNow } = vi.hoisted(() => ({
    mockCheckNow: vi.fn(async (): Promise<'applying' | 'current' | 'unknown'> => 'current'),
}));
vi.mock('$lib/services/app-update', () => ({ checkNow: mockCheckNow }));

import { isChunkLoadFailure, handleChunkFailure } from '$lib/services/chunk-failure';
import { dialogStore } from '$lib/stores';

const CHUNK_ERRORS = [
    'Failed to fetch dynamically imported module: https://host/assets/TerminalPanel-abc123.js',
    'Unable to preload CSS for /assets/DiffViewer-def456.css',
    'Importing a module script failed.',
];

const reload = vi.fn();

/** The reload happens after `checkNow()` answers, so it cannot be asserted synchronously. */
const settled = () => new Promise((resolve) => setTimeout(resolve, 0));

function setOnline(online: boolean) {
    Object.defineProperty(window.navigator, 'onLine', { value: online, configurable: true });
}

describe('a lazy chunk the browser could not fetch', () => {
    let before: typeof dialogStore.toast = null;

    beforeEach(() => {
        vi.spyOn(console, 'error').mockImplementation(() => {});
        mockCheckNow.mockClear();
        mockCheckNow.mockResolvedValue('current');
        reload.mockClear();
        sessionStorage.clear();
        setOnline(true);
        Object.defineProperty(window, 'location', {
            value: { reload },
            configurable: true,
            writable: true,
        });
        before = dialogStore.toast;
    });

    afterEach(() => {
        vi.restoreAllMocks();
    });

    it.each(CHUNK_ERRORS)('recognises %s', (message) => {
        expect(isChunkLoadFailure(new Error(message))).toBe(true);
    });

    it('leaves a failed data request alone — reloading is not the fix for it', async () => {
        for (const notAChunk of [new TypeError('Failed to fetch'), new Error('Failed to send message'), undefined]) {
            handleChunkFailure(notAChunk);
        }
        await settled();

        expect(reload).not.toHaveBeenCalled();
        expect(dialogStore.toast).toBe(before);
    });

    it('loads the server copy instead of explaining itself', async () => {
        handleChunkFailure(new Error(CHUNK_ERRORS[0]));
        await settled();

        expect(mockCheckNow).toHaveBeenCalledTimes(1);
        expect(reload).toHaveBeenCalledTimes(1);
        // Asserted literally: a missing catalog key renders the key, which also says "Updating".
        expect(dialogStore.toast?.message).toBe('Updating the app ...');
        expect(dialogStore.toast?.type).toBe('success');
    });

    it('reloads once per tab, however many clicks follow', async () => {
        handleChunkFailure(new Error(CHUNK_ERRORS[0]));
        await settled();
        handleChunkFailure(new Error(CHUNK_ERRORS[1]));
        handleChunkFailure(new Error(CHUNK_ERRORS[2]));
        await settled();

        expect(reload).toHaveBeenCalledTimes(1);
        expect(mockCheckNow).toHaveBeenCalledTimes(1);
    });

    it('does not reload on top of the reload the update already started', async () => {
        mockCheckNow.mockResolvedValueOnce('applying');

        handleChunkFailure(new Error(CHUNK_ERRORS[0]));
        await settled();

        expect(reload).not.toHaveBeenCalled();
        expect(dialogStore.toast).not.toBe(before);
    });

    // Whatever the identity said, a chunk this document asked for is missing.
    it.each(['current', 'unknown'] as const)('reloads on a %s answer', async (outcome) => {
        mockCheckNow.mockResolvedValueOnce(outcome);

        handleChunkFailure(new Error(CHUNK_ERRORS[0]));
        await settled();

        expect(reload).toHaveBeenCalledTimes(1);
    });

    it('still reloads when the check cannot answer', async () => {
        mockCheckNow.mockRejectedValueOnce(new Error('no answer'));

        handleChunkFailure(new Error(CHUNK_ERRORS[0]));
        await settled();

        expect(reload).toHaveBeenCalledTimes(1);
    });

    it('leaves the page alone while the browser is offline', async () => {
        setOnline(false);

        handleChunkFailure(new Error(CHUNK_ERRORS[0]));
        await settled();

        expect(mockCheckNow).not.toHaveBeenCalled();
        expect(reload).not.toHaveBeenCalled();
        expect(dialogStore.toast).toBe(before);
    });

    // A guard that cannot be written is a reload repeating on every load.
    it('does not reload when the guard cannot be recorded', async () => {
        vi.spyOn(Storage.prototype, 'setItem').mockImplementation(() => {
            throw new Error('QuotaExceeded');
        });

        handleChunkFailure(new Error(CHUNK_ERRORS[0]));
        await settled();

        expect(reload).not.toHaveBeenCalled();
    });
});
