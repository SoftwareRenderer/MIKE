import './app.css';
import { mount } from 'svelte';
import App from './lib/components/App.svelte';

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

mount(App, {
    target: document.getElementById('app')!,
});
