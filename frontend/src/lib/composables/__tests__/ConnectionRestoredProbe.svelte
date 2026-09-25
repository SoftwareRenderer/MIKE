<script lang="ts">
    import { useConnectionRestored } from '../use-connection-restored.svelte';

    /**
     * Test-only: a real component instance, because the rule under test is an `$effect` and the
     * moment it first runs is part of the rule. `TaskChat` registers this same composable to
     * reload what the connection was down for.
     */
    let { onRestored }: { onRestored?: () => void } = $props();

    useConnectionRestored(() => onRestored?.());
</script>

<span class="probe">probe</span>
