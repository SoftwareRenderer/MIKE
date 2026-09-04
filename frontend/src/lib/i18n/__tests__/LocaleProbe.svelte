<script lang="ts">
    import { t } from '$lib/i18n';

    /**
     * Test-only: a real rendered template, so `runtime.unit.svelte.ts` can prove that a catalog
     * swap re-renders what the browser is showing rather than merely making the next read
     * return something new. Nothing re-creates this component between the assertions.
     */
    let { count = 1 }: { count?: number } = $props();
</script>

<span class="probe">{t('i18n.test.files', { count })}</span>
