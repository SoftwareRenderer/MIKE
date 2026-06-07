<script lang="ts">
    import type { Config, Provider } from '$lib/types';
    import { updateConfig, getConfig, fetchModels as fetchModelsApi } from '$lib/api';
    import { config as configStore, showConfig } from '$lib/store';

    let { config, onClose, onSaved }: { config: Config | null; onClose: () => void; onSaved: () => void } = $props();

    let workspaceDir = $state(config?.workspace_dir || '');
    let defaultModel = $state(config?.default_model || '');
    let loading = $state(false);
    let error = $state('');

    // -- Provider management --
    interface ModelEntry {
        name: string;
        readonly: boolean; // true = fetched from API, not editable
    }

    interface ProviderDraft {
        name: string;
        display_name: string;
        url: string;
        models: ModelEntry[];
        concurrency: number;
        fetchingModels: boolean;
        fetchError: string;
    }

    /** Generate a provider name from a URL (hostname-port slug). */
    function generateProviderName(url: string): string {
        try {
            const u = new URL(url);
            const host = u.hostname.replace(/[^a-z0-9]/g, '-').replace(/-+/g, '-').replace(/^-|-$/g, '');
            const port = u.port || (u.protocol === 'https:' ? '443' : '80');
            return `${host}-${port}`;
        } catch {
            return `provider-${Date.now()}`;
        }
    }

    /** Generate a display name from a URL (TitleCase hostname:port). */
    function generateDisplayName(url: string): string {
        try {
            const u = new URL(url);
            const host = u.hostname.charAt(0).toUpperCase() + u.hostname.slice(1);
            const port = u.port || (u.protocol === 'https:' ? '443' : '80');
            return `${host}:${port}`;
        } catch {
            return 'Provider';
        }
    }

    /** Derive name and display_name from URL for a provider draft. */
    function syncProviderMeta(p: ProviderDraft) {
        if (p.url.trim()) {
            p.name = generateProviderName(p.url.trim());
            p.display_name = generateDisplayName(p.url.trim());
        } else {
            p.name = '';
            p.display_name = '';
        }
    }

    let providers = $state<ProviderDraft[]>(
        (config?.providers || []).map(p => ({
            ...p,
            concurrency: p.concurrency || 1,
            fetchingModels: false,
            fetchError: '',
            models: (p.models || []).map(m => ({ name: m, readonly: false })),
        }))
    );

    function addProvider() {
        providers.push({
            name: '',
            display_name: '',
            url: '',
            models: [],
            concurrency: 1,
            fetchingModels: false,
            fetchError: '',
        });
    }

    function removeProvider(idx: number) {
        providers.splice(idx, 1);
    }

    async function fetchModelsForProvider(idx: number) {
        const p = providers[idx];
        if (!p.url.trim()) return;
        p.fetchingModels = true;
        p.fetchError = '';
        try {
            const result = await fetchModelsApi(p.url.trim());
            if (result.error) {
                p.fetchError = result.error;
            } else {
                // Replace: clear existing models and use fetched list (readonly)
                p.models = result.models.map(m => ({ name: m, readonly: true }));
            }
        } catch (e) {
            p.fetchError = String(e);
        } finally {
            p.fetchingModels = false;
        }
    }

    function addModelToProvider(idx: number) {
        providers[idx].models.push({ name: '', readonly: false });
    }

    function removeModelFromProvider(idx: number, modelIdx: number) {
        providers[idx].models.splice(modelIdx, 1);
    }

    async function handleSave() {
        loading = true;
        error = '';
        try {
            const savedProviders: Provider[] = providers
                .filter(p => p.url.trim())
                .map(p => {
                    syncProviderMeta(p);
                    return {
                        name: p.name.trim(),
                        display_name: p.display_name.trim(),
                        url: p.url.trim(),
                        models: p.models.filter(m => m.name.trim()).map(m => m.name.trim()),
                        concurrency: p.concurrency || 1,
                    };
                });
            const cfg = await updateConfig({
                workspace_dir: workspaceDir.trim(),
                providers: savedProviders,
                default_model: defaultModel,
            });
            configStore.set(cfg);
            onSaved();
        } catch (e) {
            error = String(e);
        } finally {
            loading = false;
        }
    }

    function setDefaultModel(modelName: string) {
        if (defaultModel === modelName) {
            defaultModel = ''; // toggle off
        } else {
            defaultModel = modelName;
        }
    }

    function handleClose() {
        onClose();
    }
</script>

<div class="overlay" onclick={handleClose}>
    <div class="modal" onclick={(e) => e.stopPropagation()}>
        <div class="modal-header">
            <h2>Settings</h2>
            <button class="close-btn" onclick={handleClose}>✕</button>
        </div>
        <form class="modal-body" onsubmit={(e) => { e.preventDefault(); handleSave(); }}>
            <!-- Providers Section -->
            <div class="providers-section">
                <div class="section-header">
                    <label style="margin:0">Providers</label>
                    <button type="button" class="btn-small" onclick={addProvider}>+ Add Provider</button>
                </div>
                {#each providers as p, i}
                    <div class="provider-card">
                        <div class="provider-fields">
                            <div class="url-row">
                                <input type="text" bind:value={p.url} placeholder="http://localhost:8080/v1" style="flex:1" oninput={() => syncProviderMeta(p)} />
                                <button type="button" class="btn-fetch" onclick={() => fetchModelsForProvider(i)} disabled={p.fetchingModels || !p.url.trim()}>
                                    {p.fetchingModels ? 'Fetching...' : 'Fetch Models'}
                                </button>
                            </div>
                            {#if p.display_name}
                                <div class="provider-meta">{p.display_name}</div>
                            {/if}
                            {#if p.fetchError}
                                <div class="fetch-error">{p.fetchError}</div>
                            {/if}
                            <div class="concurrency-row">
                                <label class="concurrency-label">Concurrency</label>
                                <input type="number" bind:value={p.concurrency} min="1" max="20" class="concurrency-input" />
                            </div>
                            <div class="models-section">
                                <div class="models-header">
                                    <span class="models-label">Models ({p.models.length})</span>
                                    <button type="button" class="btn-add-model" onclick={() => addModelToProvider(i)}>+ Add</button>
                                </div>
                                {#each p.models as model, mi}
                                    {#if model.readonly}
                                        <div class="model-row model-row-readonly">
                                            <button type="button" class="btn-star" class:active={defaultModel === model.name} onclick={() => setDefaultModel(model.name)} title="Set as default model">
                                                {defaultModel === model.name ? '★' : '☆'}
                                            </button>
                                            <span class="model-name-label">{model.name}</span>
                                            <button type="button" class="btn-remove-model" onclick={() => removeModelFromProvider(i, mi)}>✕</button>
                                        </div>
                                    {:else}
                                        <div class="model-row">
                                            <button type="button" class="btn-star" class:active={model.name && defaultModel === model.name} onclick={() => model.name && setDefaultModel(model.name)} title="Set as default model">
                                                {model.name && defaultModel === model.name ? '★' : '☆'}
                                            </button>
                                            <input type="text" bind:value={model.name} placeholder="Model name" />
                                            <button type="button" class="btn-remove-model" onclick={() => removeModelFromProvider(i, mi)}>✕</button>
                                        </div>
                                    {/if}
                                {/each}
                            </div>
                        </div>
                        <button type="button" class="btn-remove-provider" onclick={() => removeProvider(i)} title="Remove provider">✕</button>
                    </div>
                {/each}
                {#if providers.length === 0}
                    <div class="providers-empty">No providers configured. Add one above.</div>
                {/if}
            </div>

            <label>
                Workspace Directory
                <input type="text" bind:value={workspaceDir} placeholder="./workspace" />
            </label>

            {#if error}
                <div class="error">{error}</div>
            {/if}

            <div class="modal-footer">
                <button type="button" onclick={handleClose}>Cancel</button>
                <button type="submit" class="btn-primary" disabled={loading}>
                    {loading ? 'Saving...' : 'Save'}
                </button>
            </div>
        </form>
    </div>
</div>

<style>
    .overlay {
        position: fixed;
        inset: 0;
        background: rgba(0,0,0,0.6);
        display: flex;
        align-items: center;
        justify-content: center;
        z-index: 100;
    }
    .modal {
        background: var(--color-surface-1);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-lg);
        width: 560px;
        max-width: 90vw;
        max-height: 90vh;
        overflow-y: auto;
    }
    .modal-header {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 16px 20px;
        border-bottom: 1px solid var(--color-border);
    }
    .modal-header h2 {
        font-size: 16px;
        font-weight: 600;
    }
    .close-btn {
        font-size: 18px;
        color: var(--color-text-secondary);
        padding: 4px;
    }
    .modal-body {
        padding: 20px;
        display: flex;
        flex-direction: column;
        gap: 16px;
    }
    label {
        display: flex;
        flex-direction: column;
        gap: 6px;
        font-size: 13px;
        font-weight: 500;
        color: var(--color-text-secondary);
    }
    input[type="text"], input[type="number"], select {
        background: var(--color-surface-2);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-md);
        padding: 8px 12px;
        color: var(--color-text-primary);
        font-size: 14px;
        outline: none;
    }
    input:focus, select:focus {
        border-color: var(--color-accent);
    }
    select {
        cursor: pointer;
    }
    .section-header {
        display: flex;
        align-items: center;
        justify-content: space-between;
        margin-bottom: 8px;
    }
    .section-header label {
        margin-bottom: 0;
    }
    .btn-small {
        padding: 4px 10px;
        border-radius: var(--radius-sm);
        background: var(--color-surface-3);
        font-size: 12px;
        cursor: pointer;
    }
    .btn-small:hover {
        background: var(--color-surface-4);
    }

    /* Providers */
    .providers-section {
        display: flex;
        flex-direction: column;
        gap: 8px;
    }
    .providers-empty {
        font-size: 12px;
        color: var(--color-text-tertiary);
        padding: 8px 0;
    }
    .provider-card {
        display: flex;
        align-items: flex-start;
        gap: 8px;
        background: var(--color-surface-2);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-md);
        padding: 12px;
    }
    .provider-fields {
        flex: 1;
        display: flex;
        flex-direction: column;
        gap: 8px;
        min-width: 0;
    }
    .field-name {
        font-family: var(--font-mono, monospace);
        font-size: 13px;
    }
    .provider-meta {
        font-size: 11px;
        color: var(--color-text-tertiary);
        font-family: var(--font-mono, monospace);
    }
    .url-row {
        display: flex;
        gap: 8px;
        align-items: center;
    }
    .btn-fetch {
        padding: 6px 12px;
        border-radius: var(--radius-sm);
        background: var(--color-accent);
        color: white;
        font-size: 12px;
        font-weight: 500;
        cursor: pointer;
        white-space: nowrap;
        flex-shrink: 0;
    }
    .btn-fetch:disabled {
        opacity: 0.5;
        cursor: not-allowed;
    }
    .fetch-error {
        font-size: 11px;
        color: var(--color-status-red);
        padding: 4px 0;
    }
    .models-section {
        display: flex;
        flex-direction: column;
        gap: 4px;
    }
    .models-header {
        display: flex;
        align-items: center;
        justify-content: space-between;
    }
    .models-label {
        font-size: 11px;
        color: var(--color-text-tertiary);
    }
    .btn-add-model {
        padding: 2px 8px;
        border-radius: var(--radius-sm);
        background: var(--color-surface-3);
        font-size: 11px;
        cursor: pointer;
    }
    .model-row {
        display: flex;
        gap: 4px;
        align-items: center;
    }
    .model-row-readonly {
        padding: 4px 8px;
        background: var(--color-surface-0);
        border-radius: var(--radius-sm);
    }
    .model-name-label {
        flex: 1;
        font-size: 13px;
        color: var(--color-text-secondary);
        font-family: var(--font-mono, monospace);
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }
    .model-row input {
        flex: 1;
        font-size: 13px;
        padding: 4px 8px;
    }
    .btn-remove-model {
        color: var(--color-status-red);
        padding: 2px 6px;
        font-size: 12px;
        flex-shrink: 0;
    }
    .btn-star {
        background: none;
        border: none;
        padding: 2px 4px;
        font-size: 14px;
        cursor: pointer;
        color: var(--color-text-tertiary);
        flex-shrink: 0;
        line-height: 1;
    }
    .btn-star:hover {
        color: var(--color-status-yellow);
    }
    .btn-star.active {
        color: var(--color-status-yellow);
    }
    .btn-remove-provider {
        color: var(--color-status-red);
        padding: 4px 8px;
        font-size: 14px;
        flex-shrink: 0;
        margin-top: 2px;
    }
    .btn-remove-provider:hover {
        background: var(--color-status-red)22;
    }

    /* Concurrency */
    .concurrency-row {
        display: flex;
        align-items: center;
        gap: 8px;
    }
    .concurrency-label {
        font-size: 11px;
        color: var(--color-text-tertiary);
        min-width: 70px;
    }
    .concurrency-input {
        width: 70px;
        font-size: 12px;
        padding: 4px 8px;
    }

    .error {
        color: var(--color-status-red);
        font-size: 13px;
    }
    .modal-footer {
        display: flex;
        justify-content: flex-end;
        gap: 8px;
        padding-top: 8px;
        border-top: 1px solid var(--color-border);
    }
    .btn-primary {
        padding: 8px 20px;
        border-radius: var(--radius-md);
        background: var(--color-accent);
        color: white;
        font-weight: 500;
        cursor: pointer;
    }
    .btn-primary:disabled {
        opacity: 0.5;
        cursor: not-allowed;
    }
    button[type="button"] {
        padding: 8px 16px;
        border-radius: var(--radius-md);
        background: var(--color-surface-3);
        color: var(--color-text-primary);
        cursor: pointer;
        border: none;
        font-family: inherit;
    }

    @media (max-width: 768px) {
        .modal {
            width: 100vw;
            max-width: none;
            max-height: 100vh;
            height: 100vh;
            border-radius: 0;
            border: none;
        }
        .modal-header {
            padding: 16px;
        }
        .modal-body {
            padding: 16px;
        }
        button[type="button"],
        .btn-primary,
        .btn-small,
        .btn-fetch,
        .btn-add-model {
            min-height: 44px;
        }
    }
</style>
