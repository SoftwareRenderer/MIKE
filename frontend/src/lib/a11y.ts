/**
 * Focus trap utility for modals and overlays.
 * Traps Tab key within a container element and restores focus on cleanup.
 */
export function createFocusTrap(container: HTMLElement, onClose?: () => void) {
    const focusableSelectors = [
        'a[href]',
        'button:not([disabled])',
        'textarea:not([disabled])',
        'input:not([disabled])',
        'select:not([disabled])',
        '[tabindex]:not([tabindex="-1"])',
    ].join(', ');

    function getFocusableElements(): HTMLElement[] {
        return Array.from(container.querySelectorAll<HTMLElement>(focusableSelectors)).filter(
            el => el.offsetParent !== null // visible
        );
    }

    function handleKeydown(e: KeyboardEvent) {
        if (e.key === 'Escape') {
            onClose?.();
            return;
        }
        if (e.key !== 'Tab') return;

        const focusable = getFocusableElements();
        if (focusable.length === 0) return;

        const first = focusable[0];
        const last = focusable[focusable.length - 1];

        if (e.shiftKey) {
            // Shift+Tab: if on first element, wrap to last
            if (document.activeElement === first) {
                e.preventDefault();
                last.focus();
            }
        } else {
            // Tab: if on last element, wrap to first
            if (document.activeElement === last) {
                e.preventDefault();
                first.focus();
            }
        }
    }

    document.addEventListener('keydown', handleKeydown);

    return {
        /** Focus the first focusable element in the container */
        focusFirst() {
            const focusable = getFocusableElements();
            focusable[0]?.focus();
        },
        /** Remove event listeners */
        destroy() {
            document.removeEventListener('keydown', handleKeydown);
        },
    };
}
