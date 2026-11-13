<script lang="ts">
    /**
     * The image built here comes from a Dockerfile that lives on the server. The build's output is streamed
     * into the progress below, and the build outlives this dialog, so that progress and the button which stops
     * it live in `BuildProgress` and are shared with the locked view.
     */
    import { t } from '$lib/i18n';
    import Spinner from '../Spinner.svelte';
    import type { Project } from '$lib/types';
    import { imageTagFor } from './drafts';
    import BuildProgress from './BuildProgress.svelte';
    import type { SandboxDraftOwner } from './draft.svelte';

    let { draft, locked, project, onBrowse }: {
        draft: SandboxDraftOwner;
        locked: boolean;
        project: Project;
        /** Open the picker that reads the Dockerfile path. */
        onBrowse: () => void;
    } = $props();

    const imageTag = $derived(imageTagFor(project.name));
    const dockerfileChanged = $derived(draft.dockerfileChanged(project));
    const busy = $derived(locked || draft.building);
</script>

<div class="dockerfile-section config-nested">
    <div class="dockerfile-header">
        <div class="config-text">
            <span class="config-label">{t('sandbox.dockerfile')}</span>
            <span class="config-desc">{t('sandbox.dockerfileDesc')}</span>
        </div>
        {#if draft.dockerfilePath}
            <div class="build-actions">
                {#if dockerfileChanged}
                    <button type="button" class="btn-build" onclick={() => draft.rebuild(project)} disabled={busy} title={t('sandbox.buildImage')}>
                        {#if draft.building}
                            <Spinner decorative />
                        {:else}
                            <svg width="14" height="14" viewBox="0 0 14 14" fill="none" aria-hidden="true">
                                <path d="M7 2L2 12h10L7 2z" stroke="currentColor" stroke-width="1.3" stroke-linejoin="round" fill="none"/>
                            </svg>
                        {/if}
                        {draft.building ? t('sandbox.building') : t('sandbox.build')}
                    </button>
                {:else}
                    <button type="button" class="btn-rebuild" onclick={() => draft.rebuild(project)} disabled={busy} title={t('sandbox.rebuildImage')}>
                        {#if draft.building}
                            <Spinner decorative />
                        {:else}
                            <svg width="14" height="14" viewBox="0 0 14 14" fill="none" aria-hidden="true">
                                <path d="M1.5 7a5.5 5.5 0 0110.2-2.8M12.5 7a5.5 5.5 0 01-10.2 2.8" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" fill="none"/>
                                <path d="M11.5 1.5v3h-3M2.5 12.5v-3h3" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round" fill="none"/>
                            </svg>
                        {/if}
                        {draft.building ? t('sandbox.building') : t('sandbox.rebuild')}
                    </button>
                {/if}
            </div>
        {/if}
    </div>

    <div class="dockerfile-path-row">
        {#if draft.dockerfilePath}
            <span class="dockerfile-path-display" title={draft.dockerfilePath}>
                <svg width="14" height="14" viewBox="0 0 14 14" fill="none" aria-hidden="true">
                    <path d="M2 2h4l1.5 1.5V12H2V2z" stroke="currentColor" stroke-width="1.2" fill="none"/>
                    <path d="M6.5 2H12V12H6.5V2z" stroke="currentColor" stroke-width="1.2" fill="none"/>
                </svg>
                <span class="dockerfile-path-text">{draft.dockerfilePath}</span>
            </span>
        {:else}
            <span class="dockerfile-path-placeholder">{t('sandbox.noDockerfileSelected')}</span>
        {/if}
        <button type="button" class="btn-browse" onclick={onBrowse} disabled={locked} title={t('sandbox.browseDockerfile')}>
            <svg width="14" height="14" viewBox="0 0 14 14" fill="none" aria-hidden="true">
                <path d="M2 4h10v7a1 1 0 01-1 1H3a1 1 0 01-1-1V4z" stroke="currentColor" stroke-width="1.2" fill="none"/>
                <path d="M4 4V2.5A1.5 1.5 0 015.5 1h3A1.5 1.5 0 0110 2.5V4" stroke="currentColor" stroke-width="1.2" fill="none" stroke-linecap="round"/>
            </svg>
            {t('sandbox.browse')}
        </button>
    </div>

    <div class="build-context-row">
        <label class="build-context-label" for="sandbox-build-context">{t('sandbox.buildContext')}</label>
        <input id="sandbox-build-context" class="build-context-input" type="text" spellcheck="false"
               autocomplete="off" placeholder="." bind:value={draft.buildContext} disabled={locked}
               title={t('sandbox.buildContextDesc')} />
    </div>

    <BuildProgress {draft} {project} />

    <p class="build-venue-note">{t('sandbox.buildVenue')}</p>

    <div class="image-tag-info">
        <svg width="12" height="12" viewBox="0 0 12 12" fill="none" aria-hidden="true">
            <circle cx="6" cy="6" r="5" stroke="currentColor" stroke-width="1"/>
            <path d="M6 3.5v0M6 5.5v3" stroke="currentColor" stroke-width="1.2" stroke-linecap="round"/>
        </svg>
        <span>{t('sandbox.imageTagLabel')} <code>{imageTag}</code></span>
    </div>
</div>

<style>
    @import '$lib/styles/sandbox-config.css';

    .dockerfile-section {
        padding-top: 8px;
        display: flex;
        flex-direction: column;
        gap: 8px;
    }

    .dockerfile-header {
        display: flex;
        align-items: flex-start;
        justify-content: space-between;
        gap: 8px;
    }

    /* Build/rebuild button, right-aligned */
    .build-actions {
        display: flex;
        align-items: center;
        gap: 8px;
        flex-shrink: 0;
    }

    .btn-build,
    .btn-rebuild,
    .btn-browse {
        display: flex;
        align-items: center;
        gap: 4px;
        border-radius: var(--radius-sm);
        font-size: var(--text-xs);
        transition: background var(--dur-base), border-color var(--dur-base);
        white-space: nowrap;
    }

    .btn-build,
    .btn-rebuild {
        padding: 3px 8px;
    }

    .btn-rebuild,
    .btn-browse {
        border: 1px solid var(--color-border);
        background: var(--color-surface-1);
        color: var(--color-text-primary);
    }

    .btn-build {
        border: 1px solid var(--color-accent);
        background: var(--color-accent-dark);
        color: var(--color-btn-text);
    }

    .btn-build:hover:not(:disabled),
    .btn-browse:hover:not(:disabled),
    .btn-rebuild:hover {
        opacity: 1;
        background: var(--color-surface-3);
        border-color: var(--color-accent);
    }

    .btn-build:hover:not(:disabled) {
        opacity: 1;
        background: var(--color-accent);
    }

    .build-venue-note {
        margin: 0;
        font-size: 11px;
        color: var(--text-tertiary, var(--text-secondary));
    }

    .build-context-row {
        display: flex;
        align-items: center;
        gap: 8px;
        flex-wrap: wrap;
    }

    .build-context-label {
        font-size: 12px;
        color: var(--text-secondary);
        white-space: nowrap;
    }

    .build-context-input {
        flex: 1 1 12rem;
        min-width: 0;
        font-family: var(--font-mono, monospace);
        font-size: 12px;
    }

    .dockerfile-path-row {
        display: flex;
        align-items: center;
        gap: 8px;
        width: 100%;
        box-sizing: border-box;
    }

    .dockerfile-path-display {
        flex: 1;
        display: flex;
        align-items: center;
        gap: 6px;
        padding: 6px 10px;
        border-radius: var(--radius-sm);
        border: 1px solid var(--color-border);
        background: var(--color-surface-1);
        color: var(--color-text-primary);
        font-size: var(--text-xs);
        font-family: var(--font-mono, monospace);
        overflow: hidden;
        min-width: 0;
    }

    .dockerfile-path-text {
        flex: 1;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
        min-width: 0;
    }

    .dockerfile-path-placeholder {
        flex: 1;
        padding: 6px 10px;
        border-radius: var(--radius-sm);
        border: 1px dashed var(--color-border);
        background: var(--color-surface-1);
        color: var(--color-text-secondary);
        font-size: var(--text-xs);
        font-style: italic;
    }

    .btn-browse {
        padding: 5px 10px;
        flex-shrink: 0;
    }

    .image-tag-info {
        display: flex;
        align-items: center;
        gap: 6px;
        font-size: var(--text-xs);
        color: var(--color-text-secondary);
    }

    .image-tag-info code {
        font-family: var(--font-mono, monospace);
        background: var(--color-surface-1);
        padding: 1px 4px;
        border-radius: var(--radius-sm);
        font-size: var(--text-2xs);
    }
</style>
