import { needsReturnReconcile, type ReturnSource } from '$lib/utils/tab-return';

/**
 * One listener set for "this window has come back", so every view that can go stale while the user is
 * elsewhere answers the same signal the same way.
 */
export interface TabReturnOptions {
    /** True while this screen could be showing something the server has moved past. */
    mayBeBehind: () => boolean;
    msSinceFrame?: () => number | null;
    /** Called at most once per return gesture. */
    onReturn: (source: ReturnSource) => void;
}

export interface TabReturn {
    destroy(): void;
}

export function createTabReturn(opts: TabReturnOptions): TabReturn {
    let lastReconcileAt = 0;
    let destroyed = false;

    function fire(source: ReturnSource): void {
        if (destroyed) return;
        if (!needsReturnReconcile({
            hidden: document.hidden,
            source,
            msSinceFrame: opts.msSinceFrame?.() ?? null,
            msSinceReconcile: Date.now() - lastReconcileAt,
            mayBeBehind: opts.mayBeBehind(),
        })) {
            return;
        }
        lastReconcileAt = Date.now();
        opts.onReturn(source);
    }

    const onVisibility = () => {
        if (!document.hidden) fire('visible');
    };
    // Some engines deliver an element's focus to a window listener; only the window's own event means
    // the user came back to this window.
    const onFocus = (e: Event) => {
        if (e.target === window) fire('focus');
    };
    const onPageShow = (e: PageTransitionEvent) => {
        if (e.persisted) fire('pageshow');
    };

    window.addEventListener('visibilitychange', onVisibility);
    window.addEventListener('focus', onFocus);
    window.addEventListener('pageshow', onPageShow);

    return {
        destroy() {
            destroyed = true;
            window.removeEventListener('visibilitychange', onVisibility);
            window.removeEventListener('focus', onFocus);
            window.removeEventListener('pageshow', onPageShow);
        },
    };
}
