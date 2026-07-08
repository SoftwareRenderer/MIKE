// PWA state — set from main.ts before App mounts, read by AppContext on init.
// This avoids the chicken-and-egg problem of context not existing yet.
export let pwaInstallPrompt: Event | null = null;
export let pwaInstalled = false;
