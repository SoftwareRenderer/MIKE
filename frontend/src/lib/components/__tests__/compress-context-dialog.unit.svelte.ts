import { describe, it, expect, afterEach, vi } from 'vitest';
import { render, cleanup, fireEvent } from '@testing-library/svelte';
import CompressContextDialog from '$lib/components/Chat/CompressContextDialog.svelte';
import { dialogStore } from '$lib/stores';
import { i18n } from '$lib/i18n/runtime.svelte';
import { DEFAULT_LOCALE } from '$lib/i18n/en';

// Svelte 5 effects (incl. the dialog's open-effect and derived updates) flush
// on the microtask queue, so give them a few ticks after render/mutations.
async function flush() {
    for (let i = 0; i < 6; i++) await Promise.resolve();
}

// Reactive source standing in for TaskChat's stream state, which the dialog
// reads through live getters.
let stats = $state({ used: 12000, limit: 32000, sessionCost: 0 });

function statValues(container: HTMLElement): string[] {
    return Array.from(container.querySelectorAll('.stat-value')).map(el => el.textContent?.trim() ?? '');
}

function donutOffset(container: HTMLElement): number {
    return Number(container.querySelector('.donut-fill')?.getAttribute('stroke-dashoffset')) || 0;
}

afterEach(() => {
    cleanup();
    stats.used = 12000;
    stats.limit = 32000;
    stats.sessionCost = 0;
    dialogStore.compressContextDialogSlot.clear();
    i18n.setCatalog(DEFAULT_LOCALE, null);
});

// [role="dialog"] is not a unique locator, and neither is a footer: find the button by its label.
function footerButton(container: HTMLElement, label: string): HTMLElement {
    const found = Array.from(container.querySelectorAll('.modal-footer button'))
        .find((b) => (b.textContent ?? '').trim() === label);
    if (!found) throw new Error(`no footer button labelled ${label}`);
    return found as HTMLElement;
}

describe('CompressContextDialog real-time stats', () => {
    it('updates the donut and stats in real time from live getters', async () => {
        const { container } = render(CompressContextDialog);
        dialogStore.showCompressContextDialog({
            used: () => stats.used,
            limit: () => stats.limit,
            sessionCost: () => stats.sessionCost,
        });
        await flush();

        // Initial values render.
        expect(statValues(container)).toEqual(['12.0K / 32.0K', '38%']);
        const initialOffset = donutOffset(container);
        expect(initialOffset).toBeGreaterThan(0);

        // Context usage grows while the dialog is open (e.g. the task keeps
        // streaming in the background) — donut and stats must follow.
        stats.used = 24000;
        await flush();

        expect(statValues(container)).toEqual(['24.0K / 32.0K', '75%']);
        expect(donutOffset(container)).toBeGreaterThan(initialOffset);
    });

    it('shows and hides the session cost row reactively', async () => {
        const { container } = render(CompressContextDialog);
        dialogStore.showCompressContextDialog({
            used: () => stats.used,
            limit: () => stats.limit,
            sessionCost: () => stats.sessionCost,
        });
        await flush();

        expect(container.textContent).not.toContain('Session cost');

        stats.sessionCost = 0.000175;
        await flush();
        expect(container.textContent).toContain('Session cost');
        expect(statValues(container)).toContain('$0.0002');

        stats.sessionCost = 0;
        await flush();
        expect(container.textContent).not.toContain('Session cost');
    });

    it('still renders snapshot numbers passed directly', async () => {
        const { container } = render(CompressContextDialog);
        dialogStore.showCompressContextDialog({ used: 8000, limit: 32000, sessionCost: 100 });
        await flush();
        expect(statValues(container)).toEqual(['8.0K / 32.0K', '25%', '$100.00']);
    });
});

describe('CompressContextDialog dismissal', () => {
    it('empties the slot it armed from and resolves the caller', async () => {
        const { container } = render(CompressContextDialog);
        const answered = dialogStore.showCompressContextDialog({ used: 8000, limit: 32000 });
        await flush();

        await fireEvent.click(footerButton(container, 'Cancel'));
        await flush();

        expect(dialogStore.compressContextDialogSlot.pending).toBeNull();
        await expect(answered).resolves.toBe(false);
    });

    it('re-labels the default confirm button when the locale changes while it is open', async () => {
        // The pack translates only the two buttons, so the runtime warns about every other key;
        // that path is covered in i18n/__tests__/runtime.unit.svelte.ts.
        const warn = vi.spyOn(console, 'warn').mockImplementation(() => {});
        const { container } = render(CompressContextDialog);
        // Opened on the slot rather than through showCompressContextDialog, so no label is filled in
        // and the dialog's own defaults are what the pack translates.
        const answered = dialogStore.compressContextDialogSlot.open({ used: 8000, limit: 32000 });
        await flush();

        expect(footerButton(container, 'Compress')).toBeTruthy();

        i18n.setCatalog('fr', { 'chat.compress.confirm': 'Compresser', 'chat.common.cancel': 'Annuler' });
        await flush();

        expect(footerButton(container, 'Compresser')).toBeTruthy();
        expect(footerButton(container, 'Annuler')).toBeTruthy();
        expect(dialogStore.compressContextDialogSlot.pending).not.toBeNull();

        await fireEvent.click(footerButton(container, 'Compresser'));
        await expect(answered).resolves.toBe(true);
        warn.mockRestore();
    });
});
