import './app.css';
import { mount } from 'svelte';
import App from './lib/components/App.svelte';
import { pwaInstallPrompt, pwaInstalled } from './lib/stores/app';

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
    pwaInstallPrompt.set(e);
});

// Detect when the PWA is installed
window.addEventListener('appinstalled', () => {
    pwaInstalled.set(true);
    pwaInstallPrompt.set(null);
});

mount(App, {
    target: document.getElementById('app')!,
});
