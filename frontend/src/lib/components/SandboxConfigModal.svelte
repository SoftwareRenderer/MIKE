<script lang="ts">
    import { onMount, onDestroy } from 'svelte';
    import type { Project } from '$lib/types';
    import { updateProject } from '$lib/api';
    import { showConfirm } from '$lib/store';
    import shieldIcon from '$lib/assets/icons/shield.svg';
    import shieldOffIcon from '$lib/assets/icons/shield-off.svg';
    import ToggleSwitch from './ToggleSwitch.svelte';

    let { project, inProgressCount, onClose, onSaved }: {
        project: Project;
        inProgressCount: number;
        onClose: () => void;
        onSaved: () => void;
    } = $props();

    let sandboxEnabled = $state(project.sandbox_enabled ?? false);
    let networkIsolation = $state(project.sandbox_network_isolation ?? false);
    let sandboxImage = $state(project.sandbox_image ?? '');
    let saving = $state(false);
    let error = $state('');
    let isLocked = $derived(inProgressCount > 0);
    let modalEl: HTMLDivElement | null = null;

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

    async function handleSave() {
        if (isLocked || saving) return;
        saving = true;
        error = '';

        const changes: { sandbox_enabled?: boolean; sandbox_network_isolation?: boolean; sandbox_image?: string } = {};
        let needsConfirmation = false;

        if (sandboxEnabled !== (project.sandbox_enabled ?? false)) {
            changes.sandbox_enabled = sandboxEnabled;
            // Turning off sandbox requires confirmation
            if (!sandboxEnabled) {
                needsConfirmation = true;
            }
        }
        if (networkIsolation !== (project.sandbox_network_isolation ?? false)) {
            changes.sandbox_network_isolation = networkIsolation;
        }
        if (sandboxImage !== (project.sandbox_image ?? '')) {
            changes.sandbox_image = sandboxImage;
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

<div class="sandbox-config-overlay" role="dialog" aria-modal="true" aria-labelledby="sandbox-config-title" tabindex="-1" onclick={handleOverlayClick}>
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
                        <span class="config-desc">Use gVisor container isolation</span>
                    </div>
                </div>
                <ToggleSwitch bind:checked={sandboxEnabled} disabled={isLocked} ariaLabel="Sandbox isolation" />
            </div>

            <!-- Network isolation toggle (only visible when sandbox is enabled) -->
            {#if sandboxEnabled}
            <div class="config-row">
                <div class="config-info">
                    <div class="config-icon">
                        {#if networkIsolation}
                            <svg width="20" height="20" viewBox="0 0 20 20" fill="none" aria-hidden="true">
                                <path d="M10 2v7M6.5 9h7M7.5 13h5M8.5 16h3" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/>
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

            <!-- Base image (only visible when sandbox is enabled) -->
            <div class="config-row config-row-input">
                <div class="config-info">
                    <div class="config-icon">
                        <svg width="20" height="20" viewBox="0 0 20 20" fill="none" aria-hidden="true">
                            <rect x="3" y="5" width="14" height="10" stroke="currentColor" stroke-width="1.3"/>
                            <path d="M6.5 5v10M10 5v10M13.5 5v10" stroke="currentColor" stroke-width="1"/>
                            <path d="M2 4.5h2.5v2H2zM2 13.5h2.5v2H2zM15.5 4.5h2.5v2h-2.5zM15.5 13.5h2.5v2h-2.5z" stroke="currentColor" stroke-width="1.1"/>
                        </svg>
                    </div>
                    <div class="config-text">
                        <span class="config-label">Base Image</span>
                        <span class="config-desc">Docker image for sandbox containers</span>
                    </div>
                </div>
                <input type="text" class="image-input" bind:value={sandboxImage} placeholder="ubuntu:latest" disabled={isLocked} />
            </div>
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
        max-width: 420px;
        width: 90%;
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
</style>
