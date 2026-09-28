/**
 * The dialog that answers a promise.
 *
 * A store slot holds the pending request, the dialog arms itself from that slot, and dismissing
 * resolves the caller's promise and empties the slot again. The slot is `PromiseSlot` in
 * `stores/dialogs.svelte.ts`: its `$state`, its promise, and the resolve handle this file reads.
 * `open`, the arming effect, the Enter-to-confirm listener and the unmount safety net live here so a
 * dialog written against a slot says only what its own body needs.
 *
 * Two rules survive here because nothing else in the file enforces them:
 *
 * - **The slot is the single source of "is this dialog open".** `dismiss()` clears the slot it was
 *   armed from and can clear no other: `clear` is the only write the dialog gets, and it takes no
 *   value. A dialog that emptied a different slot would stay open forever with nothing to arm it.
 * - **Copy stays with the caller, and only its absence is copy.** A label the caller supplied stays
 *   raw, and the default belongs in a `$derived` at the call site, never in `$state` filled in while
 *   arming: resolving it at render is what lets an already-open dialog follow a locale switch.
 */

import { onDestroy } from 'svelte';

/** The part of a pending request this composable owns. */
export interface PromiseDialogRequest {
    _resolve?: (value: boolean) => void;
}

/**
 * The store slot the pending request lives in. `get` is read reactively, so arming follows the
 * store's own `$state`.
 */
interface DialogSlot<V> {
    get: () => V | null;
    /** Empty the slot. Called on dismiss, and on nothing else. */
    clear: () => void;
}

export interface PromiseDialogOptions<V extends PromiseDialogRequest> {
    slot: DialogSlot<V>;
    /** Copy the request's own fields into the dialog's state. Runs once per arming, before `open`. */
    arm?: (value: V) => void;
    /** Enter answers the dialog the way confirming does. Both core dialogs want it. */
    confirmOnEnter?: boolean;
}

export interface PromiseDialog {
    readonly open: boolean;
    /** Resolve the caller's promise and release the slot. */
    dismiss: (value: boolean) => void;
    confirm: () => void;
    cancel: () => void;
}

export function usePromiseDialog<V extends PromiseDialogRequest>(
    options: PromiseDialogOptions<V>,
): PromiseDialog {
    // The configuration is read once: a dialog is bound to one slot for as long as it is mounted.
    const { slot, arm, confirmOnEnter = true } = options;

    let open = $state(false);
    let resolveFn: ((value: boolean) => void) | null = $state(null);

    function dismiss(value: boolean) {
        open = false;
        if (resolveFn) {
            resolveFn(value);
            resolveFn = null;
        }
        slot.clear();
    }

    $effect(() => {
        const value = slot.get();
        if (!value) return;
        arm?.(value);
        resolveFn = value._resolve ?? null;
        open = true;
    });

    // Registered once for the component's lifetime: `open` is read when a key arrives, so an
    // open/close cycle never re-subscribes.
    $effect(() => {
        if (!confirmOnEnter) return;
        const onKeydown = (e: KeyboardEvent) => {
            if (open && e.key === 'Enter') {
                e.preventDefault();
                dismiss(true);
            }
        };
        window.addEventListener('keydown', onKeydown);
        return () => window.removeEventListener('keydown', onKeydown);
    });

    onDestroy(() => {
        // A promise left pending when the dialog unmounts has no caller left to answer it.
        if (resolveFn) {
            resolveFn(false);
            resolveFn = null;
        }
    });

    return {
        get open() { return open; },
        dismiss,
        confirm: () => dismiss(true),
        cancel: () => dismiss(false),
    };
}
