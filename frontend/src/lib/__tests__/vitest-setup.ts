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
