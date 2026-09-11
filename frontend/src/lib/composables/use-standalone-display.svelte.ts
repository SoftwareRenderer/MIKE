/**
 * Shared "running in its own installed window" detection.
 * Returns `{ standalone: boolean }` via $state. Updates when the display mode changes.
 *
 * The media query is the whole signal: it answers false for a browser tab (and for a tab
 * the user put into fullscreen, which reports `display-mode: fullscreen`), and current
 * iOS Safari answers it too, so `navigator.standalone` is not needed.
 *
 * Usage:
 *   const { standalone } = useStandaloneDisplay();
 */
export function useStandaloneDisplay(): { standalone: boolean } {
    let standalone = $state(false);

    if (typeof window !== 'undefined' && window.matchMedia) {
        const mq = window.matchMedia('(display-mode: standalone)');
        standalone = mq.matches;

        const check = () => {
            standalone = mq.matches;
        };

        mq.addEventListener('change', check);

        $effect(() => {
            return () => mq.removeEventListener('change', check);
        });
    }

    return { get standalone() { return standalone; } };
}
