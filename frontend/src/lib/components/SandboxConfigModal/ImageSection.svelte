<script lang="ts">
    /**
     * Which image the sandbox container starts from: a pre-built one named by its registry, or one
     * built here from a Dockerfile. The two are alternatives — the save clears whichever this is not.
     */
    import { t } from '$lib/i18n';
    import DockerfileSection from './DockerfileSection.svelte';
    import type { SandboxDraftOwner } from './draft.svelte';
    import type { Project } from '$lib/types';

    let { draft, locked, project, onBrowse }: {
        draft: SandboxDraftOwner;
        locked: boolean;
        project: Project;
        onBrowse: () => void;
    } = $props();
</script>

<div class="config-row">
    <div class="config-info">
        <div class="config-icon">
            <svg width="20" height="20" viewBox="0 0 20 20" fill="none" aria-hidden="true">
                <rect x="3" y="5" width="14" height="10" stroke="currentColor" stroke-width="1.3"/>
                <path d="M6.5 5v10M10 5v10M13.5 5v10" stroke="currentColor" stroke-width="1"/>
                <path d="M2 4.5h2.5v2H2zM2 13.5h2.5v2H2zM15.5 4.5h2.5v2h-2.5zM15.5 13.5h2.5v2h-2.5z" stroke="currentColor" stroke-width="1.1"/>
            </svg>
        </div>
        <div class="config-text">
            <span class="config-label">{t('sandbox.imageSource')}</span>
            <span class="config-desc">{t('sandbox.imageSourceDesc')}</span>
        </div>
    </div>
    <div class="radio-group">
        <label class="radio-label" class:active={draft.imageSource === 'image'}>
            <input type="radio" name="image-source" value="image" checked={draft.imageSource === 'image'} onchange={() => draft.chooseImageSource('image')} disabled={locked} />
            <span class="radio-text">{t('sandbox.imagePrebuilt')}</span>
        </label>
        <label class="radio-label" class:active={draft.imageSource === 'dockerfile'}>
            <input type="radio" name="image-source" value="dockerfile" checked={draft.imageSource === 'dockerfile'} onchange={() => draft.chooseImageSource('dockerfile')} disabled={locked} />
            <span class="radio-text">{t('sandbox.dockerfile')}</span>
        </label>
    </div>
</div>

{#if draft.imageSource === 'image'}
    <div class="config-row config-row-input">
        <div class="config-info">
            <div class="config-text">
                <span class="config-label">{t('sandbox.imageName')}</span>
                <span class="config-desc">{t('sandbox.imageNameDesc')}</span>
            </div>
        </div>
        <!-- The placeholder is an image reference to type, not copy; the name is the aria-label. -->
        <input type="text" class="image-input" bind:value={draft.image} placeholder="docker.io/ubuntu:latest" disabled={locked} aria-label={t('sandbox.imageNameAria')} />
    </div>
{:else}
    <DockerfileSection {draft} {locked} {project} {onBrowse} />
{/if}

<style>
    @import '$lib/styles/sandbox-config.css';

    .image-input {
        flex-shrink: 0;
        width: 160px;
    }
</style>
