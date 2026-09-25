// @vitest-environment jsdom
import { describe, it, expect, afterEach, vi } from 'vitest';
import { render, fireEvent, cleanup } from '@testing-library/svelte';
import ContextUsage from '$lib/components/Chat/ContextUsage.svelte';
import ConfirmationDialog from '$lib/components/ConfirmationDialog.svelte';
import { dialogStore } from '$lib/stores';
import { i18n } from '$lib/i18n/runtime.svelte';
import { DEFAULT_LOCALE } from '$lib/i18n/en';

// Svelte 5 effects flush on the microtask queue.
async function flush() {
    for (let i = 0; i < 6; i++) await Promise.resolve();
}

/** The meter plus the app-wide confirmation dialog that answers it — the pair the shell mounts. */
function mount(props: Record<string, unknown>): { ask: HTMLElement; spinner: HTMLElement } {
    const ask = render(ConfirmationDialog);
    const meter = render(ContextUsage, { props: { used: 12000, limit: 32000, ...props } });
    const spinner = meter.container.querySelector('button.context-usage') as HTMLElement;
    return { ask: ask.container, spinner };
}

// Never a unique locator: find the answer by its label.
function answerButton(ask: HTMLElement, label: string): HTMLElement {
    const found = Array.from(ask.querySelectorAll('.modal-footer button'))
        .find((b) => (b.textContent ?? '').trim() === label);
    if (!found) throw new Error(`no footer button labelled ${label}`);
    return found as HTMLElement;
}

function actions(opts: { summarizing?: boolean; cancellable?: boolean } = {}) {
    return {
        isSummarizing: opts.summarizing ?? true,
        onSummarize: vi.fn(),
        ...(opts.cancellable === false ? {} : { onSummarizeCancel: vi.fn() }),
    };
}

afterEach(() => {
    cleanup();
    dialogStore.confirmationSlot.clear();
    i18n.setCatalog(DEFAULT_LOCALE, null);
});

describe('ContextUsage cancelling a running summarization', () => {
    it('asks first, and cancels only on the confirmed answer', async () => {
        const calls = actions();
        const { ask, spinner } = mount(calls);
        await flush();

        await fireEvent.click(spinner);
        await flush();

        expect(ask.textContent).toContain('Cancel Summarization');
        expect(calls.onSummarizeCancel).not.toHaveBeenCalled();

        await fireEvent.click(answerButton(ask, 'Cancel summarization'));
        await flush();

        expect(calls.onSummarizeCancel).toHaveBeenCalledTimes(1);
        expect(dialogStore.confirmationSlot.pending).toBeNull();
    });

    it('leaves the summarization alone when the ask is dismissed', async () => {
        const calls = actions();
        const { ask, spinner } = mount(calls);
        await flush();

        await fireEvent.click(spinner);
        await flush();
        await fireEvent.click(answerButton(ask, 'Keep running'));
        await flush();

        expect(calls.onSummarizeCancel).not.toHaveBeenCalled();
        expect(dialogStore.confirmationSlot.pending).toBeNull();
    });

    it('asks on the keyboard path too', async () => {
        const calls = actions();
        const { ask, spinner } = mount(calls);
        await flush();

        await fireEvent.keyDown(spinner, { key: 'Enter' });
        await flush();
        expect(calls.onSummarizeCancel).not.toHaveBeenCalled();

        await fireEvent.click(answerButton(ask, 'Cancel summarization'));
        await flush();
        expect(calls.onSummarizeCancel).toHaveBeenCalledTimes(1);
    });

    it('opens nothing where nothing can be cancelled', async () => {
        // An agent chat passes no cancel callback: its spinner has no cancel to confirm.
        const calls = actions({ cancellable: false });
        const { ask, spinner } = mount(calls);
        await flush();

        await fireEvent.click(spinner);
        await flush();

        expect(dialogStore.confirmationSlot.pending).toBeNull();
        expect(ask.textContent).not.toContain('Cancel Summarization');
    });

    it('still starts a summarization on a click that is not a cancellation', async () => {
        const calls = actions({ summarizing: false });
        const { spinner } = mount(calls);
        await flush();

        await fireEvent.click(spinner);
        await flush();

        expect(calls.onSummarize).toHaveBeenCalledTimes(1);
        expect(dialogStore.confirmationSlot.pending).toBeNull();
    });
});
