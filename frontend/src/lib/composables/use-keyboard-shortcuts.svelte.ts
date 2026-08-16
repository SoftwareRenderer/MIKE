/**
 * Global keyboard shortcuts (Settings → Preferences). The create-task shortcut
 * fires only when ctx.isEnabled() is true and focus is not in an editable
 * element. The open-terminal shortcut fires from any focus (including editable
 * elements) while ctx.isTerminalEnabled() is true; while the terminal is open
 * the panel itself handles the same binding and closes (see TerminalPanel).
 */
import { appStore } from '$lib/stores';
import { effectiveCreateTaskKey, effectiveOpenTerminalKey, isEditableTarget, matchShortcut } from '$lib/utils/keyboard';

interface KeyboardShortcutsContext {
    /** True only when the create-task shortcut is active (board visible, no modals). */
    isEnabled: () => boolean;
    /** Action for the create-task shortcut. */
    onCreateTask: () => void;
    /** True when the open-terminal shortcut is active (a page with a terminal button is visible, no modals). */
    isTerminalEnabled: () => boolean;
    /** Action for the open-terminal shortcut. */
    onOpenTerminal: () => void;
}

/** Wire up the keyboard shortcuts. Call from a component's setup. */
export function useKeyboardShortcuts(ctx: KeyboardShortcutsContext): void {
    $effect(() => {
        function handleKeydown(e: KeyboardEvent) {
            if (e.defaultPrevented || e.repeat) return;
            const key = effectiveCreateTaskKey(appStore.config);
            if (ctx.isEnabled() && key && !isEditableTarget(e.target) && matchShortcut(key, e)) {
                e.preventDefault();
                ctx.onCreateTask();
                return;
            }
            // Like the create-task submit binding, the open-terminal shortcut
            // fires from any focus — including editable elements — so it works
            // right after opening a task, where the chat input has focus.
            const terminalKey = effectiveOpenTerminalKey(appStore.config);
            if (ctx.isTerminalEnabled() && terminalKey && matchShortcut(terminalKey, e)) {
                e.preventDefault();
                ctx.onOpenTerminal();
            }
        }
        window.addEventListener('keydown', handleKeydown);
        return () => window.removeEventListener('keydown', handleKeydown);
    });
}
