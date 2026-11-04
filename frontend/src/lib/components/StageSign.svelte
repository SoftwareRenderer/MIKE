<!--
    The stage shapes have one home: a plus to put a path on the index, a bare rule to take it off.
    A file row, a section header, the multi-select bar and the context menu all offer the same two
    actions, so they draw these shapes rather than each keeping its own copy of the geometry.
-->
<script lang="ts">
    let { stage, size }: { stage: boolean; size: number } = $props();
</script>

<svg width={size} height={size} viewBox="0 0 16 16" fill="none" aria-hidden="true"><path d={stage ? 'M8 4v8M4 8h8' : 'M4 8h8'} stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/></svg>
