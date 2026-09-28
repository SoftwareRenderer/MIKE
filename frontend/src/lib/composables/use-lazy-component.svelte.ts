import { handleChunkFailure } from '$lib/services/chunk-failure';

/**
 * A component imported on first use instead of at module load. `loading` must end clear even when the
 * import fails, or one failed fetch becomes a panel that can never open.
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
                handleChunkFailure(e);
                loading = false;
            });
        },
    };
}
