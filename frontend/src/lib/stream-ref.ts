import type { AppStream } from './api';

/** Module-level reference to the app-level WebSocket stream.
 * Set by App.svelte. Read by TaskChat.svelte to subscribe to tasks directly. */
let _ref: AppStream | null = null;

export function setAppStreamRef(stream: AppStream | null): void {
    _ref = stream;
}

export function getAppStreamRef(): AppStream | null {
    return _ref;
}
