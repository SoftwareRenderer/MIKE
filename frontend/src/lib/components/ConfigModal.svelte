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
            <fieldset class="section">
                <div class="section-header">
                    <h3 class="section-label">Providers</h3>
                    <button type="button" class="btn-small" onclick={addProvider}>+ Add Provider</button>
                </div>
                <div class="providers-list">
                    {#each providers as p, i}
                        <div class="provider-card">
                            <div class="provider-header">
                                <div class="provider-title-row">
                                    <label class="sr-only" for={`provider-url-${i}`}>Provider URL</label>
                                    <input id={`provider-url-${i}`} type="text" bind:value={p.url} placeholder="http://localhost:8080/v1" class="url-input" oninput={() => syncProviderMeta(p)} />
                                    <button type="button" class="btn-fetch" onclick={() => fetchModelsForProvider(i)} disabled={p.fetchingModels || !p.url.trim()}>
                                        {p.fetchingModels ? 'Fetching...' : 'Fetch Models'}
                                    </button>
                                </div>
                                <button type="button" class="btn-remove-provider" onclick={() => removeProvider(i)} title="Remove provider" aria-label="Remove provider">✕</button>
                            </div>
                            {#if p.display_name}
                                <div class="provider-meta">{p.display_name}</div>
                            {/if}
                            {#if p.fetchError}
                                <div class="fetch-error">{p.fetchError}</div>
                            {/if}
                            <div class="provider-options">
                                <div class="option-row">
                                    <label class="option-label" for={`concurrency-${i}`}>Concurrency</label>
                                    <input id={`concurrency-${i}`} type="number" bind:value={p.concurrency} min="1" max="20" class="option-input" />
                                </div>
                            </div>
                            <div class="models-section">
                                <div class="models-header">
                                    <span class="models-label">Models</span>
                                    <button type="button" class="btn-add-model" onclick={() => addModelToProvider(i)}>+ Add</button>
                                </div>
                                <div class="models-list">
                                    {#each p.models as model, mi}
                                        <div class="model-row" class:readonly={model.readonly}>
                                            <button type="button" class="btn-star" class:active={model.name && defaultModel === model.name} onclick={() => model.name && setDefaultModel(model.name)} title="Set as default model" aria-label={`Set ${model.name} as default`}>
                                                {model.name && defaultModel === model.name ? '★' : '☆'}
                                            </button>
                                            {#if model.readonly}
                                                <span class="model-name-label">{model.name}</span>
                                            {:else}
                                                <input type="text" bind:value={model.name} placeholder="Model name" class="model-input" />
                                            {/if}
                                            <button type="button" class="btn-remove-model" onclick={() => removeModelFromProvider(i, mi)} aria-label={`Remove ${model.name || 'model'}`}>✕</button>
                                        </div>
                                    {/each}
                                </div>
                            </div>
                        </div>
                    {/each}
                </div>
                {#if providers.length === 0}
                    <div class="providers-empty">No providers configured. Add one above.</div>
                {/if}
            </fieldset>

            <!-- General Section -->
            <fieldset class="section">
                <h3 class="section-label">General</h3>
                <label class="field-label">
                    Workspace Directory
                    <input type="text" bind:value={workspaceDir} placeholder="./workspace" />
                </label>
            </fieldset>

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
        font-size: 20px;
        color: var(--color-text-primary);
        padding: 4px 8px;
        line-height: 1;
    }
    .modal-body {
        padding: 20px;
        display: flex;
        flex-direction: column;
        gap: 24px;
    }

    /* Screen-reader only utility */
    .sr-only {
        position: absolute;
        width: 1px;
        height: 1px;
        padding: 0;
        margin: -1px;
        overflow: hidden;
        clip: rect(0, 0, 0, 0);
        white-space: nowrap;
        border: 0;
    }

    /* Section fieldsets */
    .section {
        border: none;
        padding: 0;
        margin: 0;
        display: flex;
        flex-direction: column;
        gap: 12px;
    }

    /* Section headings */
    .section-label {
        margin: 0;
        font-size: 13px;
        font-weight: 600;
        text-transform: uppercase;
        letter-spacing: 0.05em;
        color: #B0BAC4;
    }

    /* Section header row (label + action button) */
    .section-header {
        display: flex;
        align-items: center;
        justify-content: space-between;
    }

    /* Field labels */
    .field-label {
        display: flex;
        flex-direction: column;
        gap: 6px;
        font-size: 14px;
        font-weight: 500;
        color: var(--color-text-primary);
    }

    /* Inputs */
    input[type="text"], input[type="number"], select {
        background: var(--color-surface-2);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-md);
        padding: 8px 12px;
        color: var(--color-text-primary);
        font-size: 14px;
        outline: none;
        width: 100%;
    }
    input:focus, select:focus {
        border-color: var(--color-accent);
    }
    select {
        cursor: pointer;
    }

    /* Buttons */
    .btn-small {
        padding: 6px 12px;
        border-radius: var(--radius-sm);
        background: var(--color-surface-3);
        color: var(--color-text-primary);
        font-size: 13px;
        font-weight: 500;
        cursor: pointer;
        white-space: nowrap;
    }
    .btn-small:hover {
        background: var(--color-surface-4);
    }

    /* Providers list */
    .providers-list {
        display: flex;
        flex-direction: column;
        gap: 8px;
    }
    .providers-empty {
        font-size: 14px;
        color: #B0BAC4;
        padding: 16px 0;
        text-align: center;
    }

    /* Provider card */
    .provider-card {
        background: var(--color-surface-2);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-md);
        padding: 16px;
        display: flex;
        flex-direction: column;
        gap: 10px;
    }

    /* Provider header: URL + Fetch on left, remove on right */
    .provider-header {
        display: flex;
        align-items: center;
        gap: 8px;
    }
    .provider-title-row {
        display: flex;
        align-items: center;
        gap: 8px;
        flex: 1;
        min-width: 0;
    }
    .url-input {
        flex: 1;
        min-width: 0;
    }

    /* Provider meta (display name) */
    .provider-meta {
        font-size: 12px;
        color: #B0BAC4;
        font-family: var(--font-mono, monospace);
        padding-left: 2px;
    }

    /* Fetch button */
    .btn-fetch {
        padding: 8px 14px;
        border-radius: var(--radius-sm);
        background: var(--color-accent);
        color: white;
        font-size: 13px;
        font-weight: 600;
        cursor: pointer;
        white-space: nowrap;
        flex-shrink: 0;
    }
    .btn-fetch:disabled {
        opacity: 0.5;
        cursor: not-allowed;
    }

    /* Fetch error */
    .fetch-error {
        font-size: 13px;
        color: var(--color-status-red);
        font-weight: 500;
    }

    /* Provider options (concurrency, etc.) */
    .provider-options {
        display: flex;
        flex-direction: column;
        gap: 8px;
        padding-top: 4px;
        border-top: 1px solid var(--color-border);
    }
    .option-row {
        display: flex;
        align-items: center;
        gap: 12px;
    }
    .option-label {
        font-size: 14px;
        font-weight: 500;
        color: #B0BAC4;
        flex-shrink: 0;
    }
    .option-input {
        width: 72px;
        min-width: 72px;
        flex-shrink: 0;
    }

    /* Models section */
    .models-section {
        display: flex;
        flex-direction: column;
        gap: 8px;
        padding-top: 4px;
        border-top: 1px solid var(--color-border);
    }
    .models-header {
        display: flex;
        align-items: center;
        justify-content: space-between;
    }
    .models-label {
        font-size: 13px;
        font-weight: 600;
        color: #B0BAC4;
    }
    .btn-add-model {
        padding: 4px 10px;
        border-radius: var(--radius-sm);
        background: var(--color-surface-3);
        color: var(--color-text-primary);
        font-size: 12px;
        font-weight: 500;
        cursor: pointer;
    }

    /* Models list */
    .models-list {
        display: flex;
        flex-direction: column;
        gap: 4px;
    }

    /* Model row — fixed columns for alignment */
    .model-row {
        display: grid;
        grid-template-columns: 28px 1fr 32px;
        align-items: center;
        gap: 6px;
    }
    .model-row.readonly {
        padding: 6px 8px;
        background: var(--color-surface-0);
        border-radius: var(--radius-sm);
    }

    /* Model name display */
    .model-name-label {
        font-size: 14px;
        color: #B0BAC4;
        font-family: var(--font-mono, monospace);
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
    }

    /* Model name input */
    .model-input {
        padding: 6px 8px;
        font-size: 14px;
    }

    /* Star button (default model toggle) */
    .btn-star {
        background: none;
        border: none;
        padding: 0;
        font-size: 16px;
        cursor: pointer;
        color: #B0BAC4;
        line-height: 1;
        display: flex;
        align-items: center;
        justify-content: center;
    }
    .btn-star:hover {
        color: var(--color-status-yellow);
    }
    .btn-star.active {
        color: var(--color-status-yellow);
    }

    /* Remove model button */
    .btn-remove-model {
        color: var(--color-status-red);
        padding: 0;
        font-size: 16px;
        line-height: 1;
        display: flex;
        align-items: center;
        justify-content: center;
        opacity: 0.7;
    }
    .btn-remove-model:hover {
        opacity: 1;
    }

    /* Remove provider button */
    .btn-remove-provider {
        color: var(--color-status-red);
        padding: 4px;
        font-size: 18px;
        line-height: 1;
        flex-shrink: 0;
        opacity: 0.6;
        display: flex;
        align-items: center;
        justify-content: center;
    }
    .btn-remove-provider:hover {
        opacity: 1;
    }

    .error {
        color: var(--color-status-red);
        font-size: 14px;
        font-weight: 500;
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
