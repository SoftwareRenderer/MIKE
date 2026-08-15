/**
 * Global keyboard shortcuts (Settings → Preferences). A shortcut fires only
 * when ctx.isEnabled() is true and focus is not in an editable element.
 */
import { appStore } from '$lib/stores';
import { effectiveCreateTaskKey, matchShortcut } from '$lib/utils/keyboard';

interface KeyboardShortcutsContext {
    /** True only when shortcuts should be active (board visible, no modals). */
    isEnabled: () => boolean;
    /** Action for the create-task shortcut. */
    onCreateTask: () => void;
}

function isEditableTarget(target: EventTarget | null): boolean {
    if (!(target instanceof HTMLElement)) return false;
    if (target.isContentEditable) return true;
    const tag = target.tagName;
    return tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT';
}

/** Wire up the keyboard shortcuts. Call from a component's setup. */
export function useKeyboardShortcuts(ctx: KeyboardShortcutsContext): void {
    $effect(() => {
        function handleKeydown(e: KeyboardEvent) {
            if (e.defaultPrevented || e.repeat) return;
            if (isEditableTarget(e.target)) return;
            if (!ctx.isEnabled()) return;
            const key = effectiveCreateTaskKey(appStore.config);
            if (!key || !matchShortcut(key, e)) return;
            e.preventDefault();
            ctx.onCreateTask();
        }
        window.addEventListener('keydown', handleKeydown);
        return () => window.removeEventListener('keydown', handleKeydown);
    });
}
