import {
    applySandboxImageStatus,
    idleSandboxImageState,
    type SandboxImageState,
    type SandboxImageStatusEvent,
} from '$lib/utils/sandbox-image-status';

/**
 * The project's sandbox-image preparation, as reported over the app WebSocket.
 *
 * `sandbox_image_status` reaches only the subscribed project (the channel `project_ports` uses), so
 * one current state is the honest shape — App.svelte clears it when the project changes.
 */
class SandboxImageStatusStore {
    state = $state<SandboxImageState>(idleSandboxImageState());

    /** Fold one server frame in. */
    update(event: SandboxImageStatusEvent): void {
        this.state = applySandboxImageStatus(this.state, event);
    }

    /** Forget everything — project switch, or dismissing a failure. */
    clear(): void {
        this.state = idleSandboxImageState();
    }
}

export const sandboxImageStatus = new SandboxImageStatusStore();
