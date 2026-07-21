/**
 * Composable that wires up multi-select action events from the floating bar.
 *
 * The MultiSelectBar component dispatches custom events on the window.
 * This composable listens for those events and delegates to view-specific
 * callbacks, keeping the parent component clean.
 *
 * Returns { multiSelect, setup } where setup() returns a cleanup function.
 * Call setup() in onMount and the returned cleanup in its teardown.
 */
import { MultiSelect } from './use-multi-select.svelte';

export interface MultiSelectActionsOptions {
    /** Called when the user clicks "Delete N tasks" in the floating bar. */
    onDelete: (taskIds: string[], titles: string[]) => Promise<void>;
    /** Called when the user clicks "Start N tasks" in the floating bar. */
    onStart: (taskIds: string[]) => Promise<void>;
    /** Called when the user changes the model for selected tasks in the floating bar. */
    onModelChange?: (taskIds: string[], providerUrl: string, modelName: string | undefined, reasoningEffort: string | undefined) => Promise<void>;
}

export function useMultiSelectActions(options: MultiSelectActionsOptions) {
    const multiSelect = new MultiSelect();

    function handleDelete(e: CustomEvent) {
        const taskIds = e.detail.taskIds as string[];
        const titles = e.detail.titles as string[];
        options.onDelete(taskIds, titles).finally(() => {
            multiSelect.clear();
        });
    }

    function handleStart(e: CustomEvent) {
        const taskIds = e.detail.taskIds as string[];
        options.onStart(taskIds).finally(() => {
            multiSelect.clear();
        });
    }

    function handleModelChange(e: CustomEvent) {
        // Modal dispatches empty taskIds — use the live selection instead
        const taskIds = multiSelect.toArray();
        const providerUrl = e.detail.providerUrl as string;
        const modelName = e.detail.modelName as string | undefined;
        const reasoningEffort = e.detail.reasoningEffort as string | undefined;
        if (options.onModelChange) {
            options.onModelChange(taskIds, providerUrl, modelName, reasoningEffort).finally(() => {
                multiSelect.clear();
            });
        }
    }

    /** Wire up window event listeners. Returns a cleanup function. */
    function setup(): () => void {
        window.addEventListener('multi-select-delete', handleDelete as EventListener);
        window.addEventListener('multi-select-start', handleStart as EventListener);
        window.addEventListener('multi-select-model', handleModelChange as EventListener);
        return () => {
            window.removeEventListener('multi-select-delete', handleDelete as EventListener);
            window.removeEventListener('multi-select-start', handleStart as EventListener);
            window.removeEventListener('multi-select-model', handleModelChange as EventListener);
        };
    }

    return { multiSelect, setup };
}
