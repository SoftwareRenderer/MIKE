<script lang="ts">
    /**
     * Test-only: a real component instance, because the composable under test arms from an
     * `$effect` and registers its key listener in the component's effect scope. `slot` is the
     * pending-request slot it is bound to, so two probes can prove each one clears only its own.
     */
    import { usePromiseDialog } from '../use-promise-dialog.svelte';

    interface ProbeRequest {
        text: string;
        _resolve?: (value: boolean) => void;
    }

    /** The slot shape `usePromiseDialog` takes for this request. */
    type ProbeSlot = Parameters<typeof usePromiseDialog<ProbeRequest>>[0]['slot'];

    let { slot, confirmOnEnter = true, label = '' }: {
        slot: ProbeSlot;
        confirmOnEnter?: boolean;
        /** Marks the probe's own markup, so a test can tell two of them apart. */
        label?: string;
    } = $props();

    let text = $state('');

    const dialog = usePromiseDialog({
        // Each run of the probe is bound to one slot at creation and never re-bound.
        // svelte-ignore state_referenced_locally
        slot,
        // svelte-ignore state_referenced_locally
        confirmOnEnter,
        arm: (value) => { text = value.text; },
    });
</script>

{#if dialog.open}
    <div class="dialog-{label}">
        <span class="armed-text">{text}</span>
        <button type="button" class="confirm" onclick={dialog.confirm}>confirm</button>
        <button type="button" class="cancel" onclick={dialog.cancel}>cancel</button>
    </div>
{/if}
