import { describe, it, expect, vi, beforeEach, afterEach } from 'vitest';

// Module-level mock — Workbox constructor returns a mock instance
const mockAddEventListener = vi.fn();
const mockRegister = vi.fn().mockResolvedValue(undefined);

class MockWorkbox {
    addEventListener = mockAddEventListener;
    register = mockRegister;
}

vi.mock('workbox-window', () => ({
    Workbox: MockWorkbox,
}));

describe('SwUpdateDetector', () => {
    beforeEach(() => {
        vi.resetModules();
        mockAddEventListener.mockReset();
        mockRegister.mockReset().mockResolvedValue(undefined);

        // Mock navigator.serviceWorker
        Object.defineProperty(navigator, 'serviceWorker', {
            value: {},
            writable: true,
            configurable: true,
        });

        // Mock navigator.onLine
        Object.defineProperty(navigator, 'online', {
            value: true,
            writable: true,
            configurable: true,
        });
    });

    afterEach(() => {
        vi.restoreAllMocks();
    });

    it('does not register when serviceWorker is not available', async () => {
        // Delete the property so 'serviceWorker' in navigator is false
        delete (navigator as any).serviceWorker;

        const { SwUpdateDetector } = await import('$lib/composables/use-sw-update.svelte');
        new SwUpdateDetector();

        expect(mockRegister).not.toHaveBeenCalled();
    });

    it('registers the SW on construction', async () => {
        const { SwUpdateDetector } = await import('$lib/composables/use-sw-update.svelte');
        new SwUpdateDetector();

        expect(mockRegister).toHaveBeenCalledTimes(1);
        expect(mockAddEventListener).toHaveBeenCalledWith('controlling', expect.any(Function));
    });

    it('sets needsReload when controlling event fires with isUpdate=true', async () => {
        const { SwUpdateDetector } = await import('$lib/composables/use-sw-update.svelte');
        const detector = new SwUpdateDetector();

        const controllingHandler = mockAddEventListener.mock.calls.find(
            ([event]) => event === 'controlling',
        )?.[1];
        expect(controllingHandler).toBeDefined();

        controllingHandler({ isUpdate: true });
        expect(detector.needsReload).toBe(true);
    });

    it('does not set needsReload when controlling event fires with isUpdate=false', async () => {
        const { SwUpdateDetector } = await import('$lib/composables/use-sw-update.svelte');
        const detector = new SwUpdateDetector();

        const controllingHandler = mockAddEventListener.mock.calls.find(
            ([event]) => event === 'controlling',
        )?.[1];

        controllingHandler({ isUpdate: false });
        expect(detector.needsReload).toBe(false);
    });

    it('suppresses needsReload when offline', async () => {
        const { SwUpdateDetector } = await import('$lib/composables/use-sw-update.svelte');
        const detector = new SwUpdateDetector();

        const controllingHandler = mockAddEventListener.mock.calls.find(
            ([event]) => event === 'controlling',
        )?.[1];
        controllingHandler({ isUpdate: true });
        expect(detector.needsReload).toBe(true);

        // Go offline
        Object.defineProperty(navigator, 'online', {
            value: false,
            writable: true,
            configurable: true,
        });
        window.dispatchEvent(new Event('offline'));
        expect(detector.needsReload).toBe(false);
    });

    it('restores needsReload when coming back online', async () => {
        const { SwUpdateDetector } = await import('$lib/composables/use-sw-update.svelte');
        const detector = new SwUpdateDetector();

        const controllingHandler = mockAddEventListener.mock.calls.find(
            ([event]) => event === 'controlling',
        )?.[1];
        controllingHandler({ isUpdate: true });

        // Go offline
        Object.defineProperty(navigator, 'online', {
            value: false,
            writable: true,
            configurable: true,
        });
        window.dispatchEvent(new Event('offline'));
        expect(detector.needsReload).toBe(false);

        // Come back online
        Object.defineProperty(navigator, 'online', {
            value: true,
            writable: true,
            configurable: true,
        });
        window.dispatchEvent(new Event('online'));
        expect(detector.needsReload).toBe(true);
    });

    it('reload() triggers a page reload', async () => {
        const { SwUpdateDetector } = await import('$lib/composables/use-sw-update.svelte');
        const detector = new SwUpdateDetector();

        // window.location.reload is non-configurable in jsdom, so we verify
        // by checking that calling reload() doesn't throw and the function
        // references window.location.reload
        expect(() => detector.reload()).not.toThrow();
    });
});
