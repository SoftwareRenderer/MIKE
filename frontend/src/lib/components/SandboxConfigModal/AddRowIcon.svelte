<script lang="ts">
    import Icon from '../Icon.svelte';
    import checkIcon from '$lib/assets/icons/check.svg?raw';
    import plusIcon from '$lib/assets/icons/plus.svg?raw';

    // The add-row button is a plus until a row is being edited in place, when it confirms that row.
    let { editing }: { editing: boolean } = $props();
</script>

{#if editing}
    <Icon src={checkIcon} width={14} height={14} />
{:else}
    <Icon src={plusIcon} width={14} height={14} />
{/if}
