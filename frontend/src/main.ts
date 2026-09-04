import './app.css';
import { mount } from 'svelte';
import App from './lib/components/App.svelte';
import { appStore } from './lib/stores';
import { initLocale } from './lib/i18n/runtime.svelte';

// Apply the active locale before the first mount: <html lang> drives the :lang()
// typography rules, so mounting first would paint one frame in the wrong font stack.
initLocale();

// Centralized unhandled promise rejection handler
// Catches errors from fire-and-forget async operations (storage init, etc.)
window.addEventListener('unhandledrejection', (event) => {
    const err = event.reason;
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

mount(App, {
    target: document.getElementById('app')!,
});
