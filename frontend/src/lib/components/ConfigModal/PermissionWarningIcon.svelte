<script lang="ts">
    import { t } from '$lib/i18n';
</script>

<!-- A permission that escapes the sandbox, or the manifest-level `network` capability. The name says
     what is granted; this says it is the kind of grant that reaches outside the container. -->
<svg class="perm-danger-icon" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" role="img" aria-label={t('settings.extensions.permDanger')}><title>{t('settings.extensions.permDanger')}</title><path d="m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 20h16a2 2 0 0 0 1.73-2Z"/><path d="M12 9v4"/><path d="M12 17h.01"/></svg>

<style>
    .perm-danger-icon {
        flex: none;
        color: var(--color-warning, #e0a030);
    }
</style>
