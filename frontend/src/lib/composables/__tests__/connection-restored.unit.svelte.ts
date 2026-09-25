// What a screen does when the app stream comes back: the edge, never the value it opened on.
// (.svelte.ts extension: the composable under test registers an $effect.)
import { describe, it, expect, vi, afterEach } from 'vitest';
import { render, cleanup } from '@testing-library/svelte';
import { tick } from 'svelte';
import Probe from './ConnectionRestoredProbe.svelte';
import { connectionStore } from '$lib/stores/connection.svelte';

afterEach(() => {
    cleanup();
    connectionStore.set(false);
});

describe('useConnectionRestored', () => {
    it('refetches nothing for a connection that was already up when the screen opened', async () => {
        connectionStore.set(true);
        const onRestored = vi.fn();
        render(Probe, { props: { onRestored } });
        await tick();
        expect(onRestored).not.toHaveBeenCalled();
    });

    it('refetches once across a drop and a return', async () => {
        const onRestored = vi.fn();
        render(Probe, { props: { onRestored } });
        await tick();

        connectionStore.set(false);
        await tick();
        expect(onRestored).not.toHaveBeenCalled();

        connectionStore.set(true);
        await tick();
        expect(onRestored).toHaveBeenCalledTimes(1);
    });

    it('refetches again on the next return, and not on the drop that precedes it', async () => {
        const onRestored = vi.fn();
        render(Probe, { props: { onRestored } });
        connectionStore.set(true);
        await tick();

        connectionStore.set(false);
        await tick();
        connectionStore.set(true);
        await tick();
        expect(onRestored).toHaveBeenCalledTimes(2);
    });

    it('hears nothing after the screen is gone', async () => {
        const onRestored = vi.fn();
        const { unmount } = render(Probe, { props: { onRestored } });
        await tick();

        unmount();
        connectionStore.set(true);
        await tick();
        expect(onRestored).not.toHaveBeenCalled();
    });
});
