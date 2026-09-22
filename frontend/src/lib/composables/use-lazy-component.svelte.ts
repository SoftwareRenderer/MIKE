import { reportChunkFailure } from '$lib/services/chunk-failure';

/**
 * A component imported on first use instead of at module load; a failed import means a document
 * left open across an update (`docs/frontend-updates.md`), so `loading` stays clear and the next
 * click asks again.
 */
export function useLazyComponent<T>(loadChunk: () => Promise<{ default: T }>): {
    readonly component: T | null;
    readonly loading: boolean;
    load(): void;
} {
    let component = $state<T | null>(null);
    let loading = $state(false);

    return {
        get component() { return component; },
        get loading() { return loading; },
        load() {
            if (component || loading) return;
            loading = true;
            loadChunk().then((mod) => {
                component = mod.default;
                loading = false;
            }).catch((e) => {
                reportChunkFailure(e);
                loading = false;
            });
        },
    };
}
