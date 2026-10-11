import { vi } from 'vitest';

// Patch svelte's lifecycle functions before any test file runs.
// In jsdom, Svelte's SSR runtime doesn't have onMount/onDestroy.
// We patch them on the svelte module so that components using them don't crash.

// The svelte module exports are read-only (getters), so we need to use
// Object.defineProperty to override them.
const svelteModule = await import('svelte');

Object.defineProperty(svelteModule, 'onMount', {
    value: vi.fn(),
    writable: false,
    enumerable: true,
    configurable: true,
});

Object.defineProperty(svelteModule, 'onDestroy', {
    value: vi.fn(),
    writable: false,
    enumerable: true,
    configurable: true,
});

// Node's `BroadcastChannel` reaches every worker thread of the process, so a queue notice posted in one test
// file would land in another file's queue module, whose storage is a different mock. Each file gets its own;
// `api/__tests__/queue-two-windows.unit.ts` installs a channel shared by its own windows.
if (typeof globalThis.BroadcastChannel === 'function') {
    class SameFileChannel {
        constructor(readonly name: string) {}
        addEventListener(): void {}
        removeEventListener(): void {}
        postMessage(): void {}
        close(): void {}
        dispatchEvent(): boolean { return false; }
    }
    Object.defineProperty(globalThis, 'BroadcastChannel', {
        value: SameFileChannel,
        configurable: true,
        writable: true,
    });
}

// jsdom's HTMLDialogElement declares `open` and nothing else, so showModal()/close() throw and a
// component that opens its own <dialog> never reaches the markup the test is reading. What the app
// asks of them: showModal makes it open, close makes it closed and says so.
const dialogPrototype = Object.getPrototypeOf(document.createElement('dialog')) as HTMLDialogElement;
if (typeof dialogPrototype.showModal !== 'function') {
    Object.defineProperty(dialogPrototype, 'showModal', {
        value: function (this: HTMLDialogElement) { this.open = true; },
        writable: true,
        configurable: true,
    });
    Object.defineProperty(dialogPrototype, 'close', {
        value: function (this: HTMLDialogElement) {
            if (!this.open) return;
            this.open = false;
            this.dispatchEvent(new Event('close'));
        },
        writable: true,
        configurable: true,
    });
}
