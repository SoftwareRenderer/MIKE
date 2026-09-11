/**
 * Keeps the running client on the frontend that is on disk. Two authorities, one per client: a
 * registered service worker, else the asset identity from `GET /api/version`.
 *
 * The worker stays prompt-mode (`vite.config.ts` sets no `autoUpdate`) so the page picks the moment it
 * reloads at, not the browser. Mechanism, the `waiting` states and the tests: docs/frontend-updates.md.
 */
import { Workbox } from 'workbox-window';
import { apiFetch } from '$lib/api/helpers';
import { loadAppSetting, saveAppSetting } from '$lib/storage';

const SW_URL = '/sw.js';
const VERSION_URL = '/api/version';
/** Also the reload-loop guard: see `stampToday`. */
const LAST_CHECK_DAY_KEY = 'app-update-last-check-day';

/**
 * `unknown` is not `current`: an install whose dist predates the build-id stamp answers no identity,
 * and calling that "up to date" is how a stale client is declared fresh forever.
 */
export type UpdateOutcome = 'applying' | 'current' | 'unknown';

let worker: Workbox | null = null;
let started = false;

/** Never awaited by the caller: first paint must not wait on a round-trip about this paint's own age. */
export function startAppUpdate(): void {
    if (started) return;
    started = true;
    registerWorker();
    void checkOnOpen();
}

export function checkNow(): Promise<UpdateOutcome> {
    // A manual check counts as a check, so opening the app afterwards does not run a second one.
    void saveAppSetting(LAST_CHECK_DAY_KEY, dayKey()).catch(() => {});
    return compareAndApply(() => true);
}

function registerWorker(): void {
    if (!('serviceWorker' in navigator)) return;
    worker = new Workbox(SW_URL);
    worker.addEventListener('controlling', (event) => {
        // Reload as soon as the new worker controls us: this document is the old build either way, and
        // activating deletes the previous precache, so the lazy chunks it has not fetched yet start
        // 404ing. Keeping a stale document is not the safe option.
        if (event.isUpdate) window.location.reload();
    });
    worker.register().catch((err) => {
        console.warn('[app-update] service worker registration failed; falling back to the asset identity:', err);
    });
}

async function checkOnOpen(): Promise<void> {
    // `vite dev` ships no build-id stamp, so this path has nothing to compare in a dev server.
    if (import.meta.env.DEV) return;
    if (!(await stampToday())) return;
    await compareAndApply(() => !isEditingText());
}

/**
 * Only the automatic path asks. Everything else a reload costs is in IndexedDB first — chat draft,
 * editor drafts, offline queue — except the master password, which is never stored, so a reload after
 * a manager autofills one eats the only copy. Declining costs the update a day.
 */
function isEditingText(): boolean {
    const field: unknown = document.activeElement;
    if (!(field instanceof HTMLElement)) return false;
    if (field instanceof HTMLInputElement || field instanceof HTMLTextAreaElement) {
        return field.value !== '';
    }
    // `=== true`, not truthiness: with nothing focused `activeElement` is `<body>`, whose text is the
    // whole page, and jsdom leaves `isContentEditable` undefined — either would read as "typing".
    return field.isContentEditable === true && (field.textContent ?? '').trim() !== '';
}

/**
 * Writes the stamp before the check rather than after: a check that could reload without recording
 * itself is a reload loop against a server whose answer never changes, and a write that fails is a
 * reason not to reload at all.
 */
async function stampToday(): Promise<boolean> {
    const today = dayKey();
    try {
        if ((await loadAppSetting<number>(LAST_CHECK_DAY_KEY, 0)) === today) return false;
        await saveAppSetting(LAST_CHECK_DAY_KEY, today);
        return true;
    } catch {
        return false;
    }
}

/** Compared, never shown — so no `Intl` and nothing locale-dependent at module scope. */
function dayKey(): number {
    const now = new Date();
    return (now.getFullYear() * 100 + now.getMonth() + 1) * 100 + now.getDate();
}

async function compareAndApply(mayApply: () => boolean): Promise<UpdateOutcome> {
    try {
        const registration = await existingRegistration();
        if (registration) return applyThroughWorker(registration, mayApply);
        return await applyThroughIdentity(mayApply);
    } catch {
        return 'unknown';
    }
}

async function existingRegistration(): Promise<ServiceWorkerRegistration | null> {
    if (!('serviceWorker' in navigator)) return null;
    const registration = await navigator.serviceWorker.getRegistration();
    return registration?.active ? registration : null;
}

async function applyThroughWorker(
    registration: ServiceWorkerRegistration,
    mayApply: () => boolean,
): Promise<UpdateOutcome> {
    if (!registration.waiting) await registration.update();
    if (!registration.waiting) return 'current';
    // Asked after the wait: that gap is where a password gets typed. Only the automatic caller can
    // decline, and it discards the outcome.
    if (!mayApply()) return 'unknown';
    worker?.messageSkipWaiting();
    return 'applying';
}

async function applyThroughIdentity(mayApply: () => boolean): Promise<UpdateOutcome> {
    const running = runningBuildId();
    if (!running) return 'unknown';

    const res = await apiFetch(VERSION_URL);
    if (!res.ok) return 'unknown';
    const served = (await res.json())?.assets;
    if (typeof served !== 'string' || !served) return 'unknown';

    if (running === served) return 'current';
    if (!mayApply()) return 'unknown';
    window.location.reload();
    return 'applying';
}

function runningBuildId(): string {
    const meta = document.querySelector('meta[name="app-build"]');
    return meta?.getAttribute('content') ?? '';
}
