<script lang="ts">
    /**
     * A project's Sandbox Configuration. The screen is a draft (draft.svelte.ts) and one section per
     * heading; this component owns opening it, the dirty check, and the single Save that writes the
     * whole configuration — no section saves on its own, and nothing here decides where a task runs
     * except `sandbox_enabled`, which the server stores and never re-reads.
     */
    import { onMount } from 'svelte';
    import type { Project, Task } from '$lib/types';
    import { updateProject } from '$lib/api';
    import { dialogStore } from '$lib/stores';
    import { t } from '$lib/i18n';
    import BaseModal from './BaseModal.svelte';
    import PathPicker from './PathPicker.svelte';
    import { SandboxDraftOwner } from './SandboxConfigModal/draft.svelte';
    import { isDockerfile, projectChanges } from './SandboxConfigModal/drafts';
    import CapabilitiesSection from './SandboxConfigModal/CapabilitiesSection.svelte';
    import EnvsSection from './SandboxConfigModal/EnvsSection.svelte';
    import ImageSection from './SandboxConfigModal/ImageSection.svelte';
    import IsolationRow from './SandboxConfigModal/IsolationRow.svelte';
    import LockedView from './SandboxConfigModal/LockedView.svelte';
    import MemoryRow from './SandboxConfigModal/MemoryRow.svelte';
    import MountsSection from './SandboxConfigModal/MountsSection.svelte';
    import NetworkRow from './SandboxConfigModal/NetworkRow.svelte';
    import PortsSection from './SandboxConfigModal/PortsSection.svelte';

    let { project, inProgressCount: initialInProgressCount, onClose, onSaved }: {
        project: Project;
        inProgressCount: number;
        onClose: () => void;
        onSaved: () => void;
    } = $props();

    const draft = new SandboxDraftOwner();

    // The row is the draft's starting point, so a row that changes while the screen is open reseeds
    // it — the same values the screen would have read from the row on opening.
    $effect(() => { draft.reseed(project); });

    // Local count refreshed on mount to avoid stale projectCounts
    let inProgressCount = $state(0);
    let saving = $state(false);
    let error = $state('');
    let showDirectoryPicker = $state(false);
    let showDockerfilePicker = $state(false);
    let isLocked = $derived(inProgressCount > 0);

    $effect(() => {
        inProgressCount = initialInProgressCount;
    });

    onMount(async () => {
        const tasks = await draft.loadActivePorts(project.id);
        if (tasks) {
            inProgressCount = tasks.filter((t: Task) => t.status === 'in_progress').length;
        }
    });

    async function handleSave() {
        if (isLocked || saving) return;
        // Warn if user typed something in add fields but hasn't added them
        if (draft.hasPendingRow) {
            draft.saveWarning = t('sandbox.saveWarning');
            return;
        }
        saving = true;
        error = '';
        draft.saveWarning = '';

        const { changes, needsConfirmation } = projectChanges(project, draft.snapshot());

        // Validate: dockerfile mode requires a path
        if (draft.imageSource === 'dockerfile' && !draft.dockerfilePath) {
            error = t('sandbox.dockerfileRequired');
            saving = false;
            return;
        }

        // No changes made
        if (Object.keys(changes).length === 0) {
            saving = false;
            onClose();
            return;
        }

        if (needsConfirmation) {
            const confirmed = await dialogStore.showConfirm({
                title: t('sandbox.disableTitle'),
                message: t('sandbox.disableConfirm', { name: project.name }),
                confirmText: t('sandbox.disableConfirmButton'),
                cancelText: t('sandbox.cancel'),
            });
            if (!confirmed) {
                draft.restoreSandboxEnabled(project);
                saving = false;
                return;
            }
        }

        try {
            await updateProject(project.id, changes);
            onSaved();
        } catch (e) {
            error = String(e);
        } finally {
            saving = false;
        }

        // Rebuild image in background if dockerfile mode and path changed
        if (draft.imageSource === 'dockerfile' && draft.dockerfilePath && draft.dockerfileChanged(project)) {
            draft.rebuild(project);
        }
    }
</script>

<BaseModal title={t('sandbox.title')} width={isLocked ? '480px' : '560px'} modalClass="sandbox-config-modal" footer={isLocked ? undefined : footer} onClose={onClose}>
    {#if isLocked}
        <LockedView {draft} />
    {:else}
        <div class="config-options sandbox-config">
            <IsolationRow {draft} locked={isLocked} />

            {#if draft.sandboxEnabled}
                <NetworkRow {draft} locked={isLocked} />

                <!-- Forwarding exists only while the container can be reached at all. -->
                {#if !draft.networkIsolation}
                    <PortsSection {draft} locked={isLocked} />
                {/if}

                <MountsSection {draft} locked={isLocked} onBrowse={() => showDirectoryPicker = true} />
                <EnvsSection {draft} locked={isLocked} />
                <CapabilitiesSection {draft} locked={isLocked} />
                <MemoryRow {draft} locked={isLocked} />
                <ImageSection {draft} locked={isLocked} {project} onBrowse={() => showDockerfilePicker = true} />
            {/if}
        </div>

        {#if draft.saveWarning}
            <div class="save-warning-message">{draft.saveWarning}</div>
        {/if}
        {#if error}
            <div class="error-message">{error}</div>
        {/if}
    {/if}
</BaseModal>

{#snippet footer()}
    <div class="footer-wrapper">
        <button type="button" class="btn" onclick={onClose} disabled={saving}>{t('sandbox.cancel')}</button>
        <button type="button" class="btn btn-primary" onclick={handleSave} disabled={saving || isLocked}>
            {saving ? t('sandbox.saving') : t('sandbox.save')}
        </button>
    </div>
{/snippet}

{#if showDockerfilePicker}
    <PathPicker title={t('sandbox.selectDockerfile')} initialPath={project.path} emptyLabel={t('sandbox.noDockerfilesFound')} selectTitle={t('sandbox.selectDockerfileTitle')} fileFilter={isDockerfile} onClose={() => showDockerfilePicker = false} onSelect={(path: string) => { draft.selectDockerfile(path); showDockerfilePicker = false; }} />
{/if}

{#if showDirectoryPicker}
    <PathPicker title={t('sandbox.selectDirectory')} initialPath="/home" emptyLabel={t('sandbox.noSubdirectories')} selectTitle={t('sandbox.selectDirectoryTitle')} onClose={() => showDirectoryPicker = false} onSelect={(path: string) => { draft.newMountHostPath = path; showDirectoryPicker = false; }} />
{/if}

<style>
    @import '$lib/styles/shared.css';

    :global(.sandbox-config-modal .close-btn) {
        width: 28px;
        height: 28px;
        font-size: var(--text-lg);
    }

    .config-options {
        display: flex;
        flex-direction: column;
        gap: 4px;
        padding: 12px;
        background: var(--color-surface-2);
        border-radius: var(--radius-md);
        margin-bottom: 16px;
    }

    .save-warning-message {
        padding: 8px 12px;
        background: rgb(from var(--color-status-green) r g b / 0.08);
        border: 1px solid rgb(from var(--color-status-green) r g b / 0.3);
        border-radius: var(--radius-md);
        color: var(--color-status-green);
        font-size: var(--text-sm);
        margin-bottom: 12px;
    }

    .error-message {
        padding: 8px 12px;
        background: rgb(from var(--color-status-red) r g b / 0.1);
        border: 1px solid rgb(from var(--color-status-red) r g b / 0.3);
        border-radius: var(--radius-md);
        color: var(--color-status-red);
        font-size: var(--text-sm);
        margin-bottom: 12px;
    }

    .footer-wrapper {
        display: contents;
    }
</style>
