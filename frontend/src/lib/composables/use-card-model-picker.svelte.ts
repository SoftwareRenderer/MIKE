import type { Provider } from '$lib/types';
import { updateTask } from '$lib/api';
import { board as boardStore } from '$lib/stores/app';

/** Dependencies the component must provide. */
export interface CardModelPickerDeps {
    projectId: string;
    taskId: string;
    /** Current task model name. */
    taskModelName: string;
    /** Current task provider URL. */
    taskProviderUrl: string;
    /** Current task reasoning effort. */
    taskReasoningEffort: string;
    /** Available providers from config. */
    providers: Provider[];
    /** Whether the picker is configurable (only in backlog column). */
    isConfigurable: boolean;
}

/**
 * Manages the model picker dropdown for a card.
 *
 * Handles:
 * - Open/close state and DOM ref tracking
 * - Click-outside and Escape key dismissal
 * - Dropdown positioning with overflow flip
 * - Model and agent selection with optimistic board updates
 */
export function useCardModelPicker(deps: CardModelPickerDeps) {
    let modelPickerOpen = $state(false);
    let modelBadgeRef: HTMLButtonElement | null = $state(null);
    let pickerRect = $state<{ top: number; left: number } | null>(null);

    function togglePicker(e: MouseEvent) {
        e.stopPropagation();
        if (!deps.isConfigurable) return;
        if (modelPickerOpen) {
            modelPickerOpen = false;
            pickerRect = null;
            return;
        }
        if (!modelBadgeRef || deps.providers.length === 0) return;
        const r = modelBadgeRef.getBoundingClientRect();
        pickerRect = { top: r.bottom + 4, left: r.left };
        modelPickerOpen = true;
    }

    function closePicker() {
        modelPickerOpen = false;
        pickerRect = null;
    }

    async function selectModel(providerUrl: string, modelName: string, reasoningEffort?: string) {
        if (!deps.projectId) return;
        try {
            const updated = await updateTask(deps.projectId, deps.taskId, {
                model_name: modelName,
                llama_server_url: providerUrl,
                reasoning_effort: reasoningEffort || undefined,
            });
            boardStore.update(b => {
                if (!b) return b;
                const idx = b.tasks.findIndex(t => t.id === deps.taskId);
                if (idx !== -1) b.tasks[idx] = updated;
                return { ...b };
            });
        } catch (err) {
            console.error('Failed to update model:', err);
        }
    }

    async function selectAgent() {
        if (!deps.projectId) return;
        try {
            const updated = await updateTask(deps.projectId, deps.taskId, {
                model_name: undefined,
                llama_server_url: 'pi://agent',
                reasoning_effort: undefined,
            });
            boardStore.update(b => {
                if (!b) return b;
                const idx = b.tasks.findIndex(t => t.id === deps.taskId);
                if (idx !== -1) b.tasks[idx] = updated;
                return { ...b };
            });
        } catch (err) {
            console.error('Failed to select agent:', err);
        }
        closePicker();
    }

    // Click-outside and Escape key cleanup
    $effect(() => {
        if (!modelPickerOpen) return;
        function handleClick(e: MouseEvent) {
            if (modelBadgeRef && modelBadgeRef.contains(e.target as Node)) return;
            const portal = document.querySelector('.portal-card-model-picker');
            if (portal && portal.contains(e.target as Node)) return;
            closePicker();
        }
        function handleKey(e: KeyboardEvent) {
            if (e.key === 'Escape') closePicker();
        }
        document.addEventListener('mousedown', handleClick);
        document.addEventListener('keydown', handleKey);
        return () => {
            document.removeEventListener('mousedown', handleClick);
            document.removeEventListener('keydown', handleKey);
        };
    });

    // Adjust dropdown position after render (flip if overflowing)
    $effect(() => {
        if (!modelPickerOpen || !pickerRect || !modelBadgeRef) return;
        const badge = modelBadgeRef;
        const rect = pickerRect;
        const raf = requestAnimationFrame(() => {
            const portal = document.querySelector('.portal-card-model-picker');
            if (!portal) return;
            const triggerTop = badge.getBoundingClientRect().top;
            const dropdownHeight = portal.getBoundingClientRect().height;
            const dropdownWidth = portal.getBoundingClientRect().width;
            const viewportHeight = window.visualViewport?.height || window.innerHeight;
            const viewportWidth = window.visualViewport?.width || window.innerWidth;
            let top = rect.top;
            let left = rect.left;
            const spaceBelow = viewportHeight - (triggerTop + badge.getBoundingClientRect().height);
            if (spaceBelow < dropdownHeight + 12) {
                top = triggerTop - dropdownHeight - 6;
            }
            if (left + dropdownWidth > viewportWidth - 8) {
                left = viewportWidth - dropdownWidth - 8;
            }
            if (left < 8) left = 8;
            pickerRect = { top, left };
        });
        return () => cancelAnimationFrame(raf);
    });

    return {
        modelPickerOpen,
        modelBadgeRef,
        pickerRect,
        togglePicker,
        closePicker,
        selectModel,
        selectAgent,
    };
}
