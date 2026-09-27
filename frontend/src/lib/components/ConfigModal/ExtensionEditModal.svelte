<script lang="ts">
    import { t } from '$lib/i18n';
    import BaseModal from '../BaseModal.svelte';
    import ToggleSwitch from '../ToggleSwitch.svelte';
    import ExtensionConfigFields from './ExtensionConfigFields.svelte';
    import ExtensionConnectivityFields from './ExtensionConnectivityFields.svelte';
    import MountModeRadios from './MountModeRadios.svelte';
    import type { ExtensionDraft } from './extensions-draft.svelte';
    import type { ConfigField } from '$lib/utils/extension-config';
    import { effectiveConfigFields } from '$lib/utils/extension-config';
    import { agentMountGrant, grantFingerprint } from '$lib/utils/extension-permissions';
    import { memoryLimitToInput, parseMemoryLimitMB } from '$lib/utils/memory-stats';
    import { setAgentMount, setDependencyCache, testExtensionUrl, updateExtension } from '$lib/api';

    /**
     * The Edit dialog. What it holds is a draft; what it sends on Save is three requests in one order that
     * matters: the dependency directory first (it is the change that stops a running sandbox, and a
     * refused directory must not close the dialog with the rest of the edits unsaid), then the approved
     * folders (their own endpoint, because an empty list is the way to clear an approval), then the rest
     * of the config and the LSP gates. Cancel sends nothing.
     */
    let { draft, onclose, onsaved, onerror }: {
        draft: ExtensionDraft;
        onclose: () => void;
        onsaved: () => void;
        /** Text for the tab's error banner; an empty message clears it. */
        onerror: (message: string) => void;
    } = $props();

    const ext = $derived(draft.extension);
    const dep = $derived(ext?.sandbox_config?.dependency_cache);
    const storedDep = $derived(ext?.sandbox_config?.dependency_host_path || '');
    const wantedDep = $derived(draft.dependency.trim());
    const urlFields = $derived(ext ? effectiveConfigFields(ext).filter((field) => field.type === 'url') : []);
    const grantedMounts = $derived(ext ? agentMountGrant(ext) : []);

    /** The probe is the dialog's own question about an address that may not be saved yet. */
    async function testAddress(field: ConfigField) {
        const target = draft.extension;
        if (!target || draft.probingKey) return;
        const key = field.key;
        const url = String(draft.config[key] ?? '').trim();
        if (!url) {
            draft.answerProbe(key, { success: false, message: t('settings.extensions.testNoAddress') });
            return;
        }
        draft.beginProbe(key);
        try {
            draft.answerProbe(key, await testExtensionUrl(target.id, { key, url, config: { ...draft.config } }));
        } catch (err) {
            draft.answerProbe(key, { success: false, message: (err as Error).message });
        }
    }

    async function save() {
        const target = draft.extension;
        if (!target) return;
        draft.saving = true;
        onerror('');
        draft.dependencyError = '';
        draft.mountsError = '';
        try {
            // The grant goes first: it is the change that stops a running sandbox, and a refused
            // directory must not close the dialog with the rest of the edits unsaid. Applying nothing
            // when the field still shows the stored path is what makes Cancel mean "no change".
            const grant = wantedDep;
            if (target.sandbox_config?.dependency_cache && grant !== storedDep) {
                try {
                    await setDependencyCache(target.id, grant);
                    // Read back into the open dialog, so a retry of a save that failed later does not
                    // grant the same path twice, and the card's dot is right when the modal closes.
                    if (target.sandbox_config) target.sandbox_config.dependency_host_path = grant;
                } catch (e: any) {
                    draft.dependencyError = e.message || t('settings.extensions.depRejected');
                    return;
                }
            }
            const data: { config?: Record<string, any>; manifest?: Record<string, any> } = { config: draft.config };
            // The folders go to their own endpoint, because an empty list has to mean "clear the approval".
            // This waits for Save so that Cancel still changes nothing.
            if (draft.mounts.length > 0) {
                const next = draft.mountsToSend();
                if (grantFingerprint(next) !== grantFingerprint(grantedMounts)) {
                    try {
                        const updated = await setAgentMount(target.id, next);
                        // Use the list the server saved, since it expands ~ and resolves symlinks.
                        if (updated?.config) target.config = updated.config;
                        draft.reseedMounts();
                    } catch (e: any) {
                        draft.mountsError = e?.message || t('api.setAgentMount');
                        return;
                    }
                }
            }
            if (target.type === 'lsp') {
                data.manifest = {
                    capabilities: {
                        lsp: {
                            editor_enabled: draft.lsp.editor_enabled,
                            tool_calls_enabled: draft.lsp.tool_calls_enabled,
                            chat_diagnostics_enabled: draft.lsp.chat_diagnostics_enabled,
                        },
                    },
                };
            }
            await updateExtension(target.id, data);
            draft.close();
            onsaved();
        } catch (e: any) {
            onerror(e.message || t('settings.extensions.saveFailed'));
        } finally {
            draft.saving = false;
        }
    }
</script>

{#snippet editFooter()}
    <button type="button" class="btn" onclick={() => onclose()}>{t('settings.cancel')}</button>
    <button type="button" class="btn btn-primary" onclick={() => save()} disabled={draft.saving}>
        {draft.saving ? t('settings.saving') : t('settings.save')}
    </button>
{/snippet}

{#if ext}
    <BaseModal title={t('settings.extensions.editTitle', { name: ext.manifest.display_name || ext.name })} width="480px" footer={editFooter} onClose={onclose}>
        <!-- Dependencies first: it is the one field that decides whether this server can resolve
             anything. It rides this dialog's Save rather than carrying its own Apply, so Cancel means
             nothing changed — the grant lands here, and applying it stops the running sandbox. -->
        {#if dep}
            <div class="config-fields">
                <h4>{t('settings.extensions.dependencies')}</h4>
                <div class="config-field">
                    <label class="field-label" for="edit-dep-path">{t('settings.extensions.depDirLabel')}</label>
                    <p class="field-desc">
                        {t('settings.extensions.depDirDesc.before')} <code class="ext-dep-env">{dep.env}={dep.container_path}</code> {t('settings.extensions.depDirDesc.after')}
                    </p>
                    <input
                        id="edit-dep-path"
                        type="text"
                        class="config-input ext-dep-input"
                        aria-label={t('settings.extensions.depAria', { name: ext.name })}
                        placeholder="/path/to/dependency/directory"
                        bind:value={draft.dependency}
                        disabled={draft.saving}
                    />
                    <div class="ext-dep-state">
                        {#if storedDep}
                            <span class="ext-dep-state-label">{t('settings.extensions.depMounted')}</span>
                            <code class="ext-dep-path" title={t('settings.extensions.depMountedTitle', { host: storedDep, container: dep.container_path })}>{storedDep}</code>
                            <button type="button" class="btn btn-small" onclick={() => { draft.dependency = ''; }} disabled={draft.saving}
                                title={t('settings.extensions.depUnmountHint')}>{t('settings.extensions.depUnmount')}</button>
                        {:else}
                            <span class="ext-dep-none">{t('settings.extensions.depNone')}</span>
                        {/if}
                        {#if wantedDep !== storedDep}
                            <span class="ext-dep-pending">{t('settings.extensions.depPending', { action: wantedDep === '' ? t('settings.extensions.depPendingUnmount') : storedDep ? t('settings.extensions.depPendingReplace') : t('settings.extensions.depPendingMount') })}</span>
                        {/if}
                    </div>
                    {#if draft.dependencyError}
                        <p class="ext-dep-error" role="alert">{draft.dependencyError}</p>
                    {/if}
                </div>
            </div>
        {/if}
        <!-- This is the only field here that affects other tasks' containers. An empty folder clears the
             approval. -->
        {#if draft.mounts.length > 0}
            <div class="config-fields">
                <h4>{t('settings.extensions.agentFiles')}</h4>
                <div class="config-field">
                    <label class="field-label" for="edit-agent-mount-0">{t('settings.extensions.agentMountLabel')}</label>
                    <p class="field-desc">
                        {t('settings.extensions.agentMountDesc.before')} <code>{draft.mounts[0].container_path}</code> {t('settings.extensions.agentMountDesc.after')}
                    </p>
                    {#each draft.mounts as row, i}
                        <input
                            id={`edit-agent-mount-${i}`}
                            type="text"
                            class="config-input ext-dep-input"
                            aria-label={t('settings.extensions.agentMountAria', { name: ext.name })}
                            placeholder={row.suggestion || '/path/to/the/agent/folder'}
                            bind:value={row.host_path}
                            disabled={draft.saving}
                        />
                        {#if row.suggestion && !row.host_path.trim()}
                            <button type="button" class="btn btn-small" disabled={draft.saving}
                                onclick={() => { row.host_path = row.suggestion; }}
                            >{t('settings.extensions.agentMountUse', { host: row.suggestion })}</button>
                        {/if}
                        <MountModeRadios name={`agent-mount-mode-${i}`} mode={row.mode} onselect={(mode) => { row.mode = mode; }} />
                        <!-- One row per folder: clearing this field removes this folder and leaves the
                             others approved. -->
                        {@const savedRow = grantedMounts.find((g) => g.container_path === row.container_path)}
                        <div class="ext-dep-state">
                            {#if savedRow}
                                <span class="ext-dep-state-label">{t('settings.extensions.agentMounted', { mode: savedRow.mode || 'ro' })}</span>
                                <code class="ext-dep-path" title={t('settings.extensions.agentMountedTitle', { host: savedRow.host_path, container: savedRow.container_path })}>{savedRow.host_path}</code>
                                <button type="button" class="btn btn-small" onclick={() => { row.host_path = ''; }} disabled={draft.saving}
                                    title={t('settings.extensions.agentCardUnmountHint')}>{t('settings.extensions.depUnmount')}</button>
                            {:else if !row.host_path.trim()}
                                <span class="ext-dep-none">{t('settings.extensions.agentMountNone')}</span>
                            {/if}
                            {#if row.host_path.trim() !== (savedRow?.host_path || '')}
                                <span class="ext-dep-pending">{t('settings.extensions.agentMountPending', {
                                    action: savedRow
                                        ? (row.host_path.trim() ? t('settings.extensions.depPendingReplace') : t('settings.extensions.depPendingUnmount'))
                                        : t('settings.extensions.depPendingMount'),
                                })}</span>
                            {/if}
                        </div>
                    {/each}
                    {#if draft.mountsError}
                        <p class="ext-dep-error" role="alert">{draft.mountsError}</p>
                    {/if}
                </div>
            </div>
        {/if}
        <ExtensionConfigFields ext={ext} config={draft.config} />
        <ExtensionConnectivityFields
            fields={urlFields}
            probingKey={draft.probingKey}
            answers={draft.probeAnswers}
            ontest={testAddress}
        />
        <!-- Sandbox (host-owned) — every LSP extension runs in a sandbox, so
             the memory limit is always available, declared or not. -->
        {#if ext.type === 'lsp'}
            <div class="config-fields">
                <h4>{t('settings.extensions.sandbox')}</h4>
                <div class="config-field">
                    <label class="field-label" for="edit-config-memory_limit">{t('settings.extensions.memoryLimit')}</label>
                    <p class="field-desc">{t('settings.extensions.memoryLimitDesc')}</p>
                    <input
                        id="edit-config-memory_limit"
                        type="number"
                        min="0"
                        step="1"
                        placeholder={t('settings.extensions.noLimit')}
                        value={memoryLimitToInput(draft.config.memory_limit)}
                        oninput={(e) => draft.config.memory_limit = parseMemoryLimitMB((e.target as HTMLInputElement).value)}
                        class="config-input"
                    />
                </div>
            </div>
        {/if}
        <!-- LSP Settings -->
        {#if ext.type === 'lsp'}
            <div class="config-fields">
                <h4>{t('settings.extensions.lspFeatures')}</h4>
                <div class="config-field">
                    <span class="field-label">{t('settings.extensions.editorFeatures')}</span>
                    <p class="field-desc">{t('settings.extensions.editorFeaturesDesc')}</p>
                    <ToggleSwitch
                        checked={draft.lsp.editor_enabled}
                        onChange={() => draft.lsp.editor_enabled = !draft.lsp.editor_enabled}
                        ariaLabel={t('settings.extensions.editorFeaturesAria')}
                    />
                </div>
                <div class="config-field">
                    <span class="field-label">{t('settings.extensions.toolCalls')}</span>
                    <p class="field-desc">{t('settings.extensions.toolCallsDesc')}</p>
                    <ToggleSwitch
                        checked={draft.lsp.tool_calls_enabled}
                        onChange={() => draft.lsp.tool_calls_enabled = !draft.lsp.tool_calls_enabled}
                        ariaLabel={t('settings.extensions.toolCallsAria')}
                    />
                </div>
                <div class="config-field">
                    <span class="field-label">{t('settings.extensions.chatDiagnostics')}</span>
                    <p class="field-desc">{t('settings.extensions.chatDiagnosticsDesc')}</p>
                    <ToggleSwitch
                        checked={draft.lsp.chat_diagnostics_enabled}
                        onChange={() => draft.lsp.chat_diagnostics_enabled = !draft.lsp.chat_diagnostics_enabled}
                        ariaLabel={t('settings.extensions.chatDiagnosticsAria')}
                    />
                </div>
            </div>
        {/if}
    </BaseModal>
{/if}

<style>
    .ext-dep-pending {
        font-size: var(--text-xs);
        color: var(--color-status-yellow);
    }

    .ext-dep-error {
        margin: 0.3rem 0 0;
        font-size: var(--text-xs);
        line-height: 1.45;
        color: var(--color-status-red, #f85149);
    }

    .field-label {
        display: block;
        font-size: var(--text-sm);
        color: var(--color-text-tertiary);
        margin-bottom: 0.25rem;
    }

    .config-fields {
        display: flex;
        flex-direction: column;
        gap: 1rem;
        margin-bottom: 1.5rem;
    }

    .config-fields h4 {
        margin: 0;
        font-size: var(--text-base);
        color: var(--color-text-tertiary);
        text-transform: uppercase;
        letter-spacing: 0.05em;
    }

    .config-field {
        display: flex;
        flex-direction: column;
        gap: 0.25rem;
    }

    .config-input {
        background: var(--color-bg-input);
        border: 1px solid var(--color-border-subtle);
        border-radius: var(--radius-md);
        padding: 0.5rem 0.75rem;
        color: var(--color-text-primary);
        font-size: var(--text-base);
    }

    .config-input:focus {
        outline: none;
        border-color: var(--color-accent);
    }

    .field-desc {
        color: var(--color-text-tertiary);
        font-size: var(--text-xs);
        margin: 0;
    }

    .btn-small {
        padding: 0.25rem 0.5rem;
        font-size: var(--text-xs);
    }
</style>
