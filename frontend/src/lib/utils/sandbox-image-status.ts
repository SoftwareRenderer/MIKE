/**
 * State of a project's sandbox image preparation (built from its Dockerfile, or pulled).
 *
 * Opening a project's explorer can be the first thing that touches its image, and the request that
 * triggered it waits for the image to exist — so progress arrives over the WebSocket instead of an
 * unexplained spinner. Pure (no Svelte, no store) so the transitions are testable; the reactive
 * wrapper is $lib/services/sandbox-image-status.svelte.ts. Only `sandboxImageLabel` reads the catalog —
 * the state machine below never does, so a frame folds the same whatever language is on screen.
 */

import { t } from '$lib/i18n/index.svelte';

/** A frame from the server (`sandbox_image_status`). Mirrors types.SandboxImageStatus. */
export interface SandboxImageStatusEvent {
    phase?: string;
    image?: string;
    line?: string;
    error?: string;
}

/** What the UI shows. `working` covers both building and pulling. */
export type SandboxImagePhase = 'idle' | 'working' | 'done' | 'failed';

export interface SandboxImageState {
    phase: SandboxImagePhase;
    /** True while building (false while pulling, or unknown). */
    building: boolean;
    image: string;
    /** Tail of the build/pull output, oldest first. */
    lines: string[];
    error: string;
}

/** Phases the server reports (backend/types/ws.go). */
export const SANDBOX_IMAGE_PHASES = {
    building: 'building_image',
    pulling: 'pulling_image',
    done: 'done',
    failed: 'failed',
} as const;

/** Keep only the tail: a build can print thousands of lines. */
export const MAX_SANDBOX_IMAGE_LINES = 25;

export function idleSandboxImageState(): SandboxImageState {
    return { phase: 'idle', building: false, image: '', lines: [], error: '' };
}

/**
 * Fold one server frame into the state. Returns the same object when the frame says nothing the UI
 * shows, so callers can assign it straight to a `$state` field without churning readers.
 */
export function applySandboxImageStatus(
    state: SandboxImageState,
    event: SandboxImageStatusEvent,
): SandboxImageState {
    const phase = event?.phase;
    const image = event.image || state.image;

    switch (phase) {
        case SANDBOX_IMAGE_PHASES.building:
        case SANDBOX_IMAGE_PHASES.pulling: {
            const building = phase === SANDBOX_IMAGE_PHASES.building;
            const line = event.line || '';
            // A line appends; a bare phase announcement does not clear the log. Several requests
            // join one in-flight build and each announces the phase, so clearing here would blank
            // the log repeatedly while podman is printing.
            const lines = line
                ? [...state.lines, line].slice(-MAX_SANDBOX_IMAGE_LINES)
                : state.lines;
            return { phase: 'working', building, image, lines, error: '' };
        }
        case SANDBOX_IMAGE_PHASES.done:
            // Cleared rather than shown: a banner left up after success reads like an error.
            return idleSandboxImageState();
        case SANDBOX_IMAGE_PHASES.failed:
            return {
                phase: 'failed',
                building: state.building,
                image,
                lines: state.lines,
                error: event.error || t('sandbox.imagePrepareFailed'),
            };
    }
    return state;
}

/** One-line description for the banner header. */
export function sandboxImageLabel(state: SandboxImageState): string {
    if (!state.image) return '';
    switch (state.phase) {
        case 'working':
            return state.building
                ? t('sandbox.imageBuilding', { image: state.image })
                : t('sandbox.imagePulling', { image: state.image });
        case 'failed':
            return t('sandbox.imageFailed', { image: state.image });
        default:
            return '';
    }
}

/** Whether the banner should be on screen at all. */
export function sandboxImageVisible(state: SandboxImageState): boolean {
    return state.phase === 'working' || state.phase === 'failed';
}
