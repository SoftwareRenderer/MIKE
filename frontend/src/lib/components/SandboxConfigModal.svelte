<script lang="ts">
    import { onMount, onDestroy } from 'svelte';
    import type { Project } from '$lib/types';
    import { updateProject, buildSandboxImage } from '$lib/api';
    import { showConfirm } from '$lib/store';
    import shieldIcon from '$lib/assets/icons/shield.svg';
    import shieldOffIcon from '$lib/assets/icons/shield-off.svg';
    import ToggleSwitch from './ToggleSwitch.svelte';
    import DockerfilePicker from './DockerfilePicker.svelte';

    let { project, inProgressCount, onClose, onSaved }: {
        project: Project;
        inProgressCount: number;
        onClose: () => void;
        onSaved: () => void;
    } = $props();

    let sandboxEnabled = $state(project.sandbox_enabled ?? false);
    let networkIsolation = $state(project.sandbox_network_isolation ?? false);
    let sandboxMemoryLimit = $state(project.sandbox_memory_limit ?? 1024);
    let sandboxImage = $state(project.sandbox_image ?? '');
    let sandboxDockerfilePath = $state(project.sandbox_dockerfile_path ?? '');
    let imageSource = $state<'image' | 'dockerfile'>(sandboxDockerfilePath ? 'dockerfile' : 'image');
    let dockerfileChanged = $derived(sandboxDockerfilePath !== (project.sandbox_dockerfile_path ?? ''));
    let showDockerfilePicker = $state(false);
    let imageTag = $derived(`sandbox-${project.name.replace(/ /g, '-').replace(/\//g, '-')}`);
    let saving = $state(false);
    let building = $state(false);
    let buildError = $state('');
    let error = $state('');
    let isLocked = $derived(inProgressCount > 0);
    let modalEl: HTMLDivElement | null = null;

    // Auto-enable network isolation when sandbox is toggled on
    let prevSandboxEnabled = $state(project.sandbox_enabled ?? false);
    let userToggledNetwork = $state(false);
    let networkIsolationBaseline = $state(project.sandbox_network_isolation ?? false);
    $effect(() => {
        if (sandboxEnabled && prevSandboxEnabled === false && !userToggledNetwork) {
            networkIsolation = true;
            networkIsolationBaseline = true;
        }
        prevSandboxEnabled = sandboxEnabled;
    });

    onMount(() => {
        window.addEventListener('keydown', handleKeydown);
    });

    onDestroy(() => {
        window.removeEventListener('keydown', handleKeydown);
    });

    function handleOverlayClick(e: MouseEvent) {
        if (e.target === e.currentTarget) {
            onClose();
        }
    }

    function handleKeydown(e: KeyboardEvent) {
        if (e.key === 'Escape') {
            e.preventDefault();
            onClose();
        }
    }

    function handleImageSourceChange(source: 'image' | 'dockerfile') {
        imageSource = source;
        buildError = '';
    }

    function handleBrowseDockerfile() {
        showDockerfilePicker = true;
    }

    function handleDockerfileSelected(path: string) {
        sandboxDockerfilePath = path;
        showDockerfilePicker = false;
        // Auto-build when a new Dockerfile is selected
        handleRebuild();
    }

    function handleDockerfilePickerClose() {
        showDockerfilePicker = false;
    }

    function getDockerfileName(path: string): string {
        return path.split('/').pop() || path;
    }

    async function handleRebuild() {
        if (!sandboxDockerfilePath || building) return;
        building = true;
        buildError = '';

        try {
            const result = await buildSandboxImage(project.id);
            // Show success via the image tag display
        } catch (e) {
            buildError = String(e);
        } finally {
            building = false;
        }
    }

    async function handleSave() {
        if (isLocked || saving) return;
        saving = true;
        error = '';

        const changes: {
            sandbox_enabled?: boolean;
            sandbox_network_isolation?: boolean;
            sandbox_image?: string;
            sandbox_dockerfile_path?: string;
            sandbox_memory_limit?: number;
        } = {};
        let needsConfirmation = false;

        if (sandboxEnabled !== (project.sandbox_enabled ?? false)) {
            changes.sandbox_enabled = sandboxEnabled;
            // Turning off sandbox requires confirmation
            if (!sandboxEnabled) {
                needsConfirmation = true;
            }
        }
        if (networkIsolation !== networkIsolationBaseline) {
            changes.sandbox_network_isolation = networkIsolation;
        }
        if (sandboxMemoryLimit !== (project.sandbox_memory_limit ?? 1024)) {
            changes.sandbox_memory_limit = sandboxMemoryLimit;
        }

        // Handle image source switch — clear unused field
        if (imageSource === 'image') {
            if (sandboxImage !== (project.sandbox_image ?? '')) {
                changes.sandbox_image = sandboxImage;
            }
            // Clear dockerfile path if switching away from it
            if (project.sandbox_dockerfile_path) {
                changes.sandbox_dockerfile_path = '';
            }
        } else {
            // dockerfile mode
            if (sandboxDockerfilePath !== (project.sandbox_dockerfile_path ?? '')) {
                changes.sandbox_dockerfile_path = sandboxDockerfilePath;
            }
            // Clear image if switching away from it
            if (project.sandbox_image) {
                changes.sandbox_image = '';
            }
        }

        // No changes made
        if (Object.keys(changes).length === 0) {
            saving = false;
            return;
        }

        if (needsConfirmation) {
            const confirmed = await showConfirm({
                title: 'Disable Sandbox',
                message: `Disable sandbox isolation for "${project.name}"?\n\nTasks will run without gVisor sandboxing, executing commands directly on the host.`,
                confirmText: 'Disable',
                cancelText: 'Cancel',
            });
            if (!confirmed) {
                // Revert sandboxEnabled to original value
                sandboxEnabled = project.sandbox_enabled ?? false;
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
    }
</script>

<div class="sandbox-config-overlay" role="dialog" aria-modal="true" aria-labelledby="sandbox-config-title" tabindex="-1" onclick={handleOverlayClick} onkeydown={(e) => { if (e.key === 'Escape') onClose(); }}>
    <div class="sandbox-config-dialog" bind:this={modalEl}>
        <div class="dialog-header">
            <h3 id="sandbox-config-title">Sandbox Configuration</h3>
            <button class="close-btn" onclick={onClose} aria-label="Close" title="Close">✕</button>
        </div>

        {#if isLocked}
            <div class="locked-notice">
                <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                    <rect x="3" y="7" width="10" height="7" rx="1.5" stroke="currentColor" stroke-width="1.3"/>
                    <path d="M5.5 7V5a2.5 2.5 0 015 0v2" stroke="currentColor" stroke-width="1.3" stroke-linecap="round"/>
                </svg>
                <p>Cannot change sandbox settings while tasks are In Progress.</p>
            </div>
        {/if}

        <div class="config-options" class:locked={isLocked}>
            <!-- Sandbox enabled toggle -->
            <div class="config-row">
                <div class="config-info">
                    <div class="config-icon">
                        {#if sandboxEnabled}
                            <img src={shieldIcon} width="20" height="20" alt="" />
                        {:else}
                            <img src={shieldOffIcon} width="20" height="20" alt="" />
                        {/if}
                    </div>
                    <div class="config-text">
                        <span class="config-label">Sandbox Isolation</span>
                        <span class="config-desc">Use gVisor to isolate all AI actions within a sandbox</span>
                    </div>
                </div>
                <ToggleSwitch bind:checked={sandboxEnabled} disabled={isLocked} ariaLabel="Sandbox isolation" />
            </div>

            <!-- Options visible when sandbox is enabled -->
            {#if sandboxEnabled}
            <!-- Network isolation toggle -->
            <div class="config-row" onclickcapture={() => { userToggledNetwork = true; }}>
                <div class="config-info">
                    <div class="config-icon">
                        {#if networkIsolation}
                            <svg width="20" height="20" viewBox="0 0 20 20" fill="none" aria-hidden="true">
                                <path d="M10 1v10M5 11h10M7 15h6M8.5 19h3" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
                            </svg>
                        {:else}
                            <svg width="20" height="20" viewBox="0 0 20 20" fill="none" aria-hidden="true">
                                <circle cx="10" cy="17" r="1.2" fill="currentColor"/>
                                <path d="M8 13.5Q10 12 12 13.5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" fill="none"/>
                                <path d="M4.5 10.5Q10 6 15.5 10.5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" fill="none"/>
                                <path d="M1 7.5Q10 0.5 19 7.5" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" fill="none"/>
                            </svg>
                        {/if}
                    </div>
                    <div class="config-text">
                        <span class="config-label">Network Isolation</span>
                        <span class="config-desc">Block all network access</span>
                    </div>
                </div>
                <ToggleSwitch bind:checked={networkIsolation} disabled={isLocked} ariaLabel="Network isolation" />
            </div>

            <!-- Memory limit input -->
            <div class="config-row config-row-input">
                <div class="config-info">
                    <div class="config-icon">
                        <svg width="20" height="20" viewBox="0 0 20 20" fill="none" aria-hidden="true">
                            <rect x="2" y="2" width="16" height="16" rx="1.5" stroke="currentColor" stroke-width="1.3"/>
                            <rect x="5" y="5" width="10" height="10" rx="1" stroke="currentColor" stroke-width="1"/>
                            <circle cx="4" cy="4" r="1" fill="currentColor"/>
                            <path d="M4 2v-1M8 2v-1M12 2v-1M16 2v-1M4 18v1M8 18v1M12 18v1M16 18v1M2 4h-1M2 8h-1M2 12h-1M2 16h-1M18 4h1M18 8h1M18 12h1M18 16h1" stroke="currentColor" stroke-width="1.2" stroke-linecap="round"/>
                        </svg>
                    </div>
                    <div class="config-text">
                        <span class="config-label">Memory Limit</span>
                        <span class="config-desc">Max container memory (MB)</span>
                    </div>
                </div>
                <div class="memory-input-group">
                    <input type="number" class="memory-input" bind:value={sandboxMemoryLimit} min="0" step="256" disabled={isLocked} aria-label="Maximum container memory in MB" />
                    <span class="memory-unit">MB</span>
                </div>
            </div>

            <!-- Image source selector -->
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
                        <span class="config-label">Image Source</span>
                        <span class="config-desc">Container image for sandbox</span>
                    </div>
                </div>
                <div class="radio-group">
                    <label class="radio-label" class:active={imageSource === 'image'}>
                        <input type="radio" name="image-source" value="image" checked={imageSource === 'image'} onchange={() => handleImageSourceChange('image')} disabled={isLocked} />
                        <span class="radio-text">Pre-built</span>
                    </label>
                    <label class="radio-label" class:active={imageSource === 'dockerfile'}>
                        <input type="radio" name="image-source" value="dockerfile" checked={imageSource === 'dockerfile'} onchange={() => handleImageSourceChange('dockerfile')} disabled={isLocked} />
                        <span class="radio-text">Dockerfile</span>
                    </label>
                </div>
            </div>

            <!-- Pre-built image input (only visible when image source is 'image') -->
            {#if imageSource === 'image'}
            <div class="config-row config-row-input">
                <div class="config-info">
                    <div class="config-text">
                        <span class="config-label">Image Name</span>
                        <span class="config-desc">Docker image for sandbox containers</span>
                    </div>
                </div>
                <input type="text" class="image-input" bind:value={sandboxImage} placeholder="ubuntu:latest" disabled={isLocked} aria-label="Sandbox image name" />
            </div>
            {:else}
            <!-- Dockerfile section -->
            <div class="dockerfile-section">
                <div class="dockerfile-header">
                    <div class="config-text">
                        <span class="config-label">Dockerfile</span>
                        <span class="config-desc">Select a Dockerfile on the server</span>
                    </div>
                    {#if sandboxDockerfilePath && !dockerfileChanged}
                    <button type="button" class="btn-rebuild" onclick={handleRebuild} disabled={isLocked || building} title="Rebuild image">
                        {#if building}
                            <svg class="spin" width="14" height="14" viewBox="0 0 14 14" fill="none" aria-hidden="true">
                                <circle cx="7" cy="7" r="5" stroke="currentColor" stroke-width="1.5" stroke-dasharray="20 10" stroke-linecap="round"/>
                            </svg>
                        {:else}
                            <svg width="14" height="14" viewBox="0 0 14 14" fill="none" aria-hidden="true">
                                <path d="M1.5 7a5.5 5.5 0 0110.2-2.8M12.5 7a5.5 5.5 0 01-10.2 2.8" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" fill="none"/>
                                <path d="M11.5 1.5v3h-3M2.5 12.5v-3h3" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round" fill="none"/>
                            </svg>
                        {/if}
                        {building ? 'Building...' : 'Rebuild'}
                    </button>
                    {/if}
                </div>

                <div class="dockerfile-path-row">
                    {#if sandboxDockerfilePath}
                        <span class="dockerfile-path-display" title={sandboxDockerfilePath}>
                            <svg width="14" height="14" viewBox="0 0 14 14" fill="none" aria-hidden="true">
                                <path d="M2 2h4l1.5 1.5V12H2V2z" stroke="currentColor" stroke-width="1.2" fill="none"/>
                                <path d="M6.5 2H12V12H6.5V2z" stroke="currentColor" stroke-width="1.2" fill="none"/>
                            </svg>
                            <span class="dockerfile-path-text">{sandboxDockerfilePath}</span>
                        </span>
                    {:else}
                        <span class="dockerfile-path-placeholder">No Dockerfile selected</span>
                    {/if}
                    <button type="button" class="btn-browse" onclick={handleBrowseDockerfile} disabled={isLocked} title="Browse for Dockerfile">
                        <svg width="14" height="14" viewBox="0 0 14 14" fill="none" aria-hidden="true">
                            <path d="M2 4h10v7a1 1 0 01-1 1H3a1 1 0 01-1-1V4z" stroke="currentColor" stroke-width="1.2" fill="none"/>
                            <path d="M4 4V2.5A1.5 1.5 0 015.5 1h3A1.5 1.5 0 0110 2.5V4" stroke="currentColor" stroke-width="1.2" fill="none" stroke-linecap="round"/>
                        </svg>
                        Browse
                    </button>
                </div>

                {#if buildError}
                <div class="build-error">{buildError}</div>
                {/if}

                <div class="image-tag-info">
                    <svg width="12" height="12" viewBox="0 0 12 12" fill="none" aria-hidden="true">
                        <circle cx="6" cy="6" r="5" stroke="currentColor" stroke-width="1"/>
                        <path d="M6 3.5v0M6 5.5v3" stroke="currentColor" stroke-width="1.2" stroke-linecap="round"/>
                    </svg>
                    <span>Image tag: <code>{imageTag}</code></span>
                </div>
            </div>
            {/if}
            {/if}
        </div>

        {#if error}
            <div class="error-message">{error}</div>
        {/if}

        <div class="dialog-actions">
            <button type="button" class="btn-cancel" onclick={onClose} disabled={saving}>Cancel</button>
            <button type="button" class="btn-save" onclick={handleSave} disabled={saving || isLocked}>
                {saving ? 'Saving...' : 'Save'}
            </button>
        </div>
    </div>
</div>

{#if showDockerfilePicker}
    <DockerfilePicker initialPath={project.path} onClose={handleDockerfilePickerClose} onSelect={handleDockerfileSelected} />
{/if}

<style>
    .sandbox-config-overlay {
        position: fixed;
        inset: 0;
        background: rgba(0, 0, 0, 0.6);
        display: flex;
        align-items: center;
        justify-content: center;
        z-index: 1000;
        animation: fade-in 0.15s ease-out;
    }

    .sandbox-config-dialog {
        background: var(--color-surface-1);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-lg);
        padding: 20px 24px;
        max-width: 480px;
        width: 90%;
        max-height: 90vh;
        overflow-y: auto;
        animation: scale-in 0.15s ease-out;
    }

    .dialog-header {
        display: flex;
        align-items: center;
        justify-content: space-between;
        margin-bottom: 16px;
    }

    .dialog-header h3 {
        font-size: 0.875rem;
        font-weight: 600;
        margin: 0;
        color: var(--color-text-primary);
    }

    .close-btn {
        width: 28px;
        height: 28px;
        display: flex;
        align-items: center;
        justify-content: center;
        border-radius: var(--radius-sm);
        background: transparent;
        color: var(--color-text-secondary);
        font-size: 1rem;
        cursor: pointer;
        transition: background 0.15s, color 0.15s;
    }

    .close-btn:hover {
        background: var(--color-surface-3);
        color: var(--color-text-primary);
    }

    .locked-notice {
        display: flex;
        align-items: center;
        gap: 10px;
        padding: 10px 14px;
        background: rgba(210, 153, 34, 0.1);
        border: 1px solid rgba(210, 153, 34, 0.3);
        border-radius: var(--radius-md);
        margin-bottom: 16px;
        color: var(--color-status-yellow, #d29922);
        font-size: 0.8125rem;
    }

    .locked-notice p {
        margin: 0;
        line-height: 1.4;
    }

    .config-options {
        display: flex;
        flex-direction: column;
        gap: 4px;
        padding: 12px;
        background: var(--color-surface-2);
        border-radius: var(--radius-md);
        margin-bottom: 16px;
        transition: opacity 0.15s;
    }

    .config-options.locked {
        opacity: 0.5;
        pointer-events: none;
    }

    .config-row {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 8px 0;
    }

    .config-row:not(:last-child) {
        border-bottom: 1px solid var(--color-border);
    }

    .config-row-input {
        gap: 12px;
    }

    .image-input {
        flex-shrink: 0;
        width: 160px;
        padding: 4px 8px;
        border-radius: var(--radius-sm);
        border: 1px solid var(--color-border);
        background: var(--color-surface-1);
        color: var(--color-text-primary);
        font-size: 0.75rem;
        font-family: var(--font-mono, monospace);
        transition: border-color 0.15s;
    }

    .image-input:focus {
        outline: none;
        border-color: var(--color-accent);
    }

    .image-input:disabled {
        opacity: 0.5;
    }

    .memory-input-group {
        display: flex;
        align-items: center;
        gap: 4px;
        flex-shrink: 0;
    }

    .memory-input {
        width: 80px;
        padding: 4px 8px;
        border-radius: var(--radius-sm);
        border: 1px solid var(--color-border);
        background: var(--color-surface-1);
        color: var(--color-text-primary);
        font-size: 0.75rem;
        font-family: var(--font-mono, monospace);
        transition: border-color 0.15s;
        -moz-appearance: textfield;
        appearance: textfield;
    }

    .memory-input::-webkit-outer-spin-button,
    .memory-input::-webkit-inner-spin-button {
        -webkit-appearance: none;
        margin: 0;
    }

    .memory-input:focus {
        outline: none;
        border-color: var(--color-accent);
    }

    .memory-input:disabled {
        opacity: 0.5;
    }

    .memory-unit {
        font-size: 0.75rem;
        color: var(--color-text-secondary);
        white-space: nowrap;
    }

    .config-info {
        display: flex;
        align-items: center;
        gap: 12px;
        flex: 1;
        min-width: 0;
    }

    .config-icon {
        flex-shrink: 0;
        width: 24px;
        height: 24px;
        display: flex;
        align-items: center;
        justify-content: center;
        color: var(--color-primary);
    }

    .config-text {
        display: flex;
        flex-direction: column;
        min-width: 0;
    }

    .config-label {
        font-size: 0.8125rem;
        font-weight: 500;
        color: var(--color-text-primary);
    }

    .config-desc {
        font-size: 0.75rem;
        color: var(--color-text-secondary);
        margin-top: 1px;
    }

    /* Radio group */
    .radio-group {
        display: flex;
        gap: 0;
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
        overflow: hidden;
        flex-shrink: 0;
    }

    .radio-label {
        display: flex;
        align-items: center;
        gap: 4px;
        padding: 3px 10px;
        font-size: 0.75rem;
        color: var(--color-text-secondary);
        cursor: pointer;
        transition: background 0.15s, color 0.15s;
        user-select: none;
    }

    .radio-label:first-child {
        border-right: 1px solid var(--color-border);
    }

    .radio-label.active {
        background: var(--color-accent);
        color: white;
    }

    .radio-label:hover:not(.active) {
        background: var(--color-surface-3);
    }

    .radio-label input[type="radio"] {
        display: none;
    }

    .radio-label:disabled {
        opacity: 0.5;
        cursor: not-allowed;
    }

    .radio-text {
        white-space: nowrap;
    }

    /* Dockerfile section */
    .dockerfile-section {
        padding: 8px 0 0;
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

    .btn-rebuild {
        display: flex;
        align-items: center;
        gap: 4px;
        padding: 3px 8px;
        border-radius: var(--radius-sm);
        border: 1px solid var(--color-border);
        background: var(--color-surface-1);
        color: var(--color-text-primary);
        font-size: 0.75rem;
        cursor: pointer;
        transition: background 0.15s, border-color 0.15s;
        white-space: nowrap;
    }

    .btn-rebuild:hover {
        background: var(--color-surface-3);
        border-color: var(--color-accent);
    }

    .btn-rebuild:disabled {
        opacity: 0.5;
        cursor: not-allowed;
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
        font-size: 0.75rem;
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
        font-size: 0.75rem;
        font-style: italic;
    }

    .btn-browse {
        display: flex;
        align-items: center;
        gap: 4px;
        padding: 5px 10px;
        border-radius: var(--radius-sm);
        border: 1px solid var(--color-border);
        background: var(--color-surface-1);
        color: var(--color-text-primary);
        font-size: 0.75rem;
        cursor: pointer;
        transition: background 0.15s, border-color 0.15s;
        white-space: nowrap;
        flex-shrink: 0;
    }

    .btn-browse:hover:not(:disabled) {
        background: var(--color-surface-3);
        border-color: var(--color-accent);
    }

    .btn-browse:disabled {
        opacity: 0.5;
        cursor: not-allowed;
    }

    .image-tag-info {
        display: flex;
        align-items: center;
        gap: 6px;
        font-size: 0.75rem;
        color: var(--color-text-secondary);
    }

    .image-tag-info code {
        font-family: var(--font-mono, monospace);
        background: var(--color-surface-1);
        padding: 1px 4px;
        border-radius: 3px;
        font-size: 0.6875rem;
    }

    .build-error {
        padding: 6px 10px;
        background: rgba(248, 81, 73, 0.1);
        border: 1px solid rgba(248, 81, 73, 0.3);
        border-radius: var(--radius-sm);
        color: var(--color-status-red);
        font-size: 0.75rem;
    }

    .error-message {
        padding: 8px 12px;
        background: rgba(248, 81, 73, 0.1);
        border: 1px solid rgba(248, 81, 73, 0.3);
        border-radius: var(--radius-md);
        color: var(--color-status-red);
        font-size: 0.8125rem;
        margin-bottom: 12px;
    }

    .dialog-actions {
        display: flex;
        gap: 8px;
        justify-content: flex-end;
    }

    .btn-cancel {
        padding: 6px 14px;
        border-radius: var(--radius-sm);
        border: 1px solid var(--color-border);
        background: var(--color-surface-2);
        color: var(--color-text-primary);
        font-size: 0.8125rem;
        cursor: pointer;
        transition: background 0.15s;
    }

    .btn-cancel:hover {
        background: var(--color-surface-3);
    }

    .btn-save {
        padding: 6px 14px;
        border-radius: var(--radius-sm);
        border: none;
        background: var(--color-accent);
        color: white;
        font-size: 0.8125rem;
        font-weight: 500;
        cursor: pointer;
        transition: background 0.15s;
    }

    .btn-save:hover {
        background: var(--color-accent-hover);
    }

    .btn-save:disabled,
    .btn-cancel:disabled {
        opacity: 0.5;
        cursor: not-allowed;
    }

    @keyframes fade-in {
        from { opacity: 0; }
        to { opacity: 1; }
    }

    @keyframes scale-in {
        from {
            opacity: 0;
            transform: scale(0.95);
        }
        to {
            opacity: 1;
            transform: scale(1);
        }
    }

    @keyframes spin {
        from { transform: rotate(0deg); }
        to { transform: rotate(360deg); }
    }

    .spin {
        animation: spin 1s linear infinite;
    }
</style>
