/**
 * Shared mobile detection composable.
 * Returns `{ isMobile: boolean }` via $state. Updates on resize.
 *
 * Usage:
 *   const { isMobile } = useIsMobile();
 */
export function useIsMobile(breakpoint = 768): { isMobile: boolean } {
    let isMobile = $state(false);

    if (typeof window !== 'undefined') {
        isMobile = window.matchMedia(`(max-width: ${breakpoint}px)`).matches;

        const check = () => {
            isMobile = window.matchMedia(`(max-width: ${breakpoint}px)`).matches;
        };

        window.matchMedia(`(max-width: ${breakpoint}px)`).addEventListener('change', check);
        window.addEventListener('resize', check);

        $effect(() => {
            return () => {
                window.matchMedia(`(max-width: ${breakpoint}px)`).removeEventListener('change', check);
                window.removeEventListener('resize', check);
            };
        });
    }

    return { get isMobile() { return isMobile; } };
}
