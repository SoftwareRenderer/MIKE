/**
 * Click-outside, for the things that close when the pointer lands somewhere else.
 *
 * The listener is added once, when the component mounts, and removed when it is destroyed — an
 * open/close cycle never re-subscribes. The container is read per event instead of being captured,
 * so a menu that mounts and unmounts (`bind:this` on an `{#if}` block) needs no re-registration.
 *
 * `event` has no default because the choices are not interchangeable:
 *   - `click` fires on the common ancestor of a drag's start and end, so a press inside a menu that
 *     is released outside closes it; `mousedown`/`pointerdown` close on the press instead, which is
 *     what a menu that would otherwise swallow the drag wants.
 *   - `pointerdown` with `capture: true` runs before the target's own default action, which is the
 *     only way a tap on another control dismisses the open thing in time.
 * A site that needs one of those keeps needing it, so it says so at the call.
 */

/** The events that can decide "this press was outside". */
type OutsideEvent = 'click' | 'mousedown' | 'pointerdown';

export interface ClickOutsideOptions {
    event: OutsideEvent;
    /** Capture phase: runs before the target handles the press. */
    capture?: boolean;
    /**
     * Regions that belong to the open thing but are not inside the container — a dropdown portalled
     * out of the component, a tooltip anchored elsewhere. Read per event, so it may answer `null`
     * while that node is unmounted.
     */
    alsoInside?: () => Iterable<Node | null | undefined>;
}

export function useClickOutside(
    /** The element a press must land inside to count as inside. `null` while the open thing is
     *  unmounted, which is not "outside": there is nothing to close. */
    container: () => Node | null | undefined,
    onOutside: (event: MouseEvent | PointerEvent) => void,
    { event, capture = false, alsoInside }: ClickOutsideOptions,
): void {
    $effect(() => {
        const handler = (e: Event) => {
            const target = e.target;
            if (!(target instanceof Node)) return;
            const root = container();
            if (!root || root.contains(target)) return;
            if (alsoInside) {
                for (const region of alsoInside()) {
                    if (region?.contains(target)) return;
                }
            }
            onOutside(e as MouseEvent | PointerEvent);
        };
        document.addEventListener(event, handler, capture);
        return () => document.removeEventListener(event, handler, capture);
    });
}
