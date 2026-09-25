<script lang="ts">
    /** The discard of a row being edited, in the add row of every list this screen edits. */
    import { t } from '$lib/i18n';

    let { onclick }: { onclick: () => void } = $props();
</script>

<button type="button" class="btn-cancel-edit" onclick={onclick} title={t('sandbox.cancelEdit')}>
    <svg width="12" height="12" viewBox="0 0 12 12" fill="none" aria-hidden="true">
        <path d="M2 2L10 10M10 2L2 10" stroke="currentColor" stroke-width="1.3" stroke-linecap="round"/>
    </svg>
</button>

<style>
    @import '$lib/styles/sandbox-config.css';
</style>
