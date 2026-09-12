import type { SandboxMemoryStats, Task } from '$lib/types';
import { appStore, dialogStore, updateTaskInBoard } from '$lib/stores';
import { sandboxStatsStore } from '$lib/stores/sandbox-stats.svelte';
import { dismissMemoryAlert } from '$lib/api';
import { memoryAlertPayloadOf, visibleMemoryAlert } from '$lib/utils/memory-stats';
import { t } from '$lib/i18n';

/** The out-of-memory alert one task shows, and the one way it is dismissed. */
export function useMemoryAlert(
    getTask: () => Task,
    getProjectLimitMB: () => number | null,
    getStats: () => SandboxMemoryStats | null | undefined = () => sandboxStatsStore.get(getTask().id),
) {
    // Read through getters: the board replaces the task object on every frame.
    const alert = $derived(visibleMemoryAlert(getTask(), getProjectLimitMB(), getStats()));

    /** The click's numbers: the ack covers exactly the alert being shown. */
    async function dismiss(): Promise<void> {
        const shown = alert;
        if (!shown) return;
        const task = getTask();
        const ack = memoryAlertPayloadOf(shown.alert);
        // Stamped locally so the bar clears on the click; the write tells the other devices.
        updateTaskInBoard(appStore, task.id, { ...task, memory_alert_ack: ack });
        try {
            // The response is not applied: the broadcast answers this too, and a queued action returns a stub.
            await dismissMemoryAlert(task.project_id, task.id, ack);
        } catch (e) {
            // This tab dropped the alert; the server did not, which is what the toast says.
            dialogStore.showToast(t('memory.alert.dismissFailed', { error: String(e) }), 'error');
        }
    }

    return { get alert() { return alert; }, dismiss };
}
