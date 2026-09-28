<script lang="ts">
    import { useClickOutside } from '../use-click-outside.svelte';

    /**
     * Test-only: a real component instance, because the composable under test registers its
     * listener in the component's effect scope and must stop hearing presses when that scope is
     * destroyed. The box lives in an `{#if}` so `container()` is `null` exactly while it is closed.
     */
    let { event = 'mousedown', capture = false, portal = false, onClose }: {
        event?: Parameters<typeof useClickOutside>[2]['event'];
        capture?: boolean;
        /** Render a second node that is not inside the box, as a portalled dropdown is not. */
        portal?: boolean;
        onClose?: () => void;
    } = $props();

    let open = $state(false);
    let box: HTMLDivElement | null = $state(null);
    let portalEl: HTMLDivElement | null = $state(null);

    // Each run of the probe is configured before it is created and never re-configured, so the
    // listener it registers is built from the values it was created with.
    useClickOutside(() => box, () => { onClose?.(); open = false; }, {
        // svelte-ignore state_referenced_locally
        event,
        // svelte-ignore state_referenced_locally
        capture,
        alsoInside: () => [portalEl],
    });
</script>

<button class="toggle" onclick={() => { open = !open; }}>toggle</button>
{#if open}
    <div class="box" bind:this={box}><span class="inside">inside</span></div>
    {#if portal}
        <div class="portal" bind:this={portalEl}>portal</div>
    {/if}
{/if}
