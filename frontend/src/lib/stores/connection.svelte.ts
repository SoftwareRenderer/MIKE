/**
 * Whether the app-level stream (`/api/stream`) is up right now.
 *
 * Written only by `handleWsConnection` in `App.svelte` — a reader must never set it.
 * Consumers (e.g. `TaskChat`) react to the false→true edge to refresh data that
 * changed while the connection was down; the value at mount is not that edge.
 */
class WsConnectionStore {
    private connectedNow = $state(false);

    get connected(): boolean {
        return this.connectedNow;
    }

    set(connected: boolean): void {
        this.connectedNow = connected;
    }
}

export const connectionStore = new WsConnectionStore();
