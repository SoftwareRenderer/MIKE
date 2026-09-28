/** Chrome fires `resume` when it unfreezes a page; other engines fire nothing, so the clock in
 * `page-suspension` is the fallback. A busy-loop freeze is not a real unfreeze, so no e2e spec reaches this
 * listener. */
import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';
import { createTabReturn } from '$lib/composables/use-tab-return';

type WatchOptions = Parameters<typeof createTabReturn>[0];
type Watcher = ReturnType<typeof createTabReturn>;

describe('createTabReturn', () => {
    const watchers: Watcher[] = [];

    function watch(opts: WatchOptions): Watcher {
        const watcher = createTabReturn(opts);
        watchers.push(watcher);
        return watcher;
    }

    beforeEach(() => {
        // jsdom reports a page that is not visible at all; every case here is a window the user can see.
        Object.defineProperty(document, 'hidden', { configurable: true, get: () => false });
    });
    afterEach(() => {
        while (watchers.length) watchers.pop()!.destroy();
    });

    it('treats a thawed page as a return for the view that asks the server', () => {
        const onReturn = vi.fn();
        watch({ revalidateOnReturn: true, onReturn });

        document.dispatchEvent(new Event('resume'));

        expect(onReturn).toHaveBeenCalledWith('resume');
    });

    it('leaves a view that can answer for itself out of the thaw', () => {
        const onReturn = vi.fn();
        watch({ mayBeBehind: () => true, msSinceFrame: () => 0, onReturn });

        document.dispatchEvent(new Event('resume'));

        expect(onReturn).not.toHaveBeenCalled();
    });

    it('revalidates a visible return even when the screen cannot prove it fell behind', () => {
        const onReturn = vi.fn();
        watch({ mayBeBehind: () => false, revalidateOnReturn: true, msSinceFrame: () => 0, onReturn });

        window.dispatchEvent(new Event('visibilitychange'));

        expect(onReturn).toHaveBeenCalledWith('visible');
    });

    it('answers one signal per return gesture', () => {
        const onReturn = vi.fn();
        watch({ revalidateOnReturn: true, onReturn });

        document.dispatchEvent(new Event('resume'));
        document.dispatchEvent(new Event('resume'));

        expect(onReturn).toHaveBeenCalledTimes(1);
    });

    it('hears nothing once the view has gone', () => {
        const onReturn = vi.fn();
        const watcher = watch({ revalidateOnReturn: true, onReturn });

        watcher.destroy();
        document.dispatchEvent(new Event('resume'));

        expect(onReturn).not.toHaveBeenCalled();
    });
});
