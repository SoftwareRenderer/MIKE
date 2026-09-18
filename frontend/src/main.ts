import './app.css';
import { mount } from 'svelte';
import App from './lib/components/App.svelte';
import { appStore } from './lib/stores';
import { applyCachedCatalog } from './lib/i18n/boot';
import { startAppUpdate } from './lib/services/app-update';
import { watchNotificationClicks } from './lib/utils/notifications';
import { isChunkLoadFailure, reportChunkFailure } from './lib/services/chunk-failure';

// Centralized unhandled promise rejection handler
// Catches errors from fire-and-forget async operations (storage init, etc.)
window.addEventListener('unhandledrejection', (event) => {
    const err = event.reason;
    if (isChunkLoadFailure(err)) {
        reportChunkFailure(err);
        return;
    }
    if (err && err.stack) {
        console.warn('[Unhandled Promise Rejection] Suppressed. Stack:', err.stack);
    } else {
        console.warn('[Unhandled Promise Rejection] Suppressed:', err);
    }
    event.preventDefault();
});

// Capture the PWA install prompt event so the UI can trigger it on demand
window.addEventListener('beforeinstallprompt', (e) => {
    // Prevent Chrome 67+ from showing the automatic install prompt
    e.preventDefault();
    // Store the event for later use
    appStore.pwaInstallPrompt = e;
});

// Detect when the PWA is installed
window.addEventListener('appinstalled', () => {
    appStore.pwaInstalled = true;
    appStore.pwaInstallPrompt = null;
});

// Before the mount and before the first await: a click the worker posts has to find a listener.
watchNotificationClicks();

/**
 * Mount after the last resolved language has been applied.
 *
 * Which i18n pack is active is a server round-trip away, so without this the
 * first frame is English and then switches. `applyCachedCatalog` applies what the
 * live extension list said last time (bounded, and never throws), and the first
 * `loadExtensions()` corrects it — including back to English. Mirrors the
 * `kanban-theme-css` pre-paint step in index.html, which exists for the same
 * reason; see frontend/src/lib/i18n/boot.ts.
 */
async function boot(): Promise<void> {
    await applyCachedCatalog();
    mount(App, {
        target: document.getElementById('app')!,
    });
    // After the mount: a reload is only silent if there is nothing on screen to yank away.
    startAppUpdate();
}

void boot();
