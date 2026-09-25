import { untrack } from 'svelte';
import { connectionStore } from '$lib/stores/connection.svelte';

/**
 * Run `onRestored` each time the app stream comes back — the false→true edge only.
 *
 * The value read at mount is a state, not a return: a screen that opened on a live connection
 * has missed nothing, and refetching then would reload a transcript that is already current.
 * The callback runs untracked, so this effect depends on the flag and on nothing else — what the
 * refetch reads and writes is not its business. Register it from component scope; it owns an
 * `$effect`. `docs/reconnection.md`.
 */
export function useConnectionRestored(onRestored: () => void): void {
    let wasDisconnected = false;
    $effect(() => {
        const connected = connectionStore.connected;
        if (connected && wasDisconnected) untrack(onRestored);
        wasDisconnected = !connected;
    });
}
