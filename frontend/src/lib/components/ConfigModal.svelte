<script lang="ts">
    import type { Config, Provider } from '$lib/types';
    import { updateConfig, getConfig, testConnection as testConnectionApi, fetchModels as fetchModelsApi } from '$lib/api';
    import { config as configStore, showConfig } from '$lib/store';

    let { config, onClose, onSaved }: { config: Config | null; onClose: () => void; onSaved: () => void } = $props();

    let serverUrl = $state(config?.llama_server_url || '');
    let modelName = $state(config?.model_name || '');
    let workspaceDir = $state(config?.workspace_dir || '');
    let defaultProvider = $state(config?.default_provider || '');
    let loading = $state(false);
    let testing = $state(false);
    let testResult = $state('');
    let error = $state('');

    interface EndpointLimit { url: string; max: number; }
    let endpointLimits = $state<EndpointLimit[]>([]);

    if (config?.endpoint_limits) {
        endpointLimits = Object.entries(config.endpoint_limits).map(([url, max]) => ({ url, max }));
    }

    function addEndpointLimit() {
        endpointLimits.push({ url: '', max: 2 });
    }

    function removeEndpointLimit(idx: number) {
        endpointLimits.splice(idx, 1);
    }

    // -- Provider management --
    interface ProviderDraft {
        name: string;
        display_name: string;
        url: string;
        models: string[];
        fetchingModels: boolean;
        fetchError: string;
    }

    let providers = $state<ProviderDraft[]>(
        (config?.providers || []).map(p => ({
            ...p,
            fetchingModels: false,
            fetchError: '',
        }))
    );

    function addProvider() {
        providers.push({
            name: '',
            display_name: '',
            url: '',
            models: [],
            fetchingModels: false,
            fetchError: '',
        });
    }

    function removeProvider(idx: number) {
        if (providers[idx].name === defaultProvider) {
            defaultProvider = '';
        }
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
                // Merge: add new models that aren't already in the list
                const existing = new Set(p.models);
                for (const m of result.models) {
                    if (!existing.has(m)) {
                        p.models.push(m);
                    }
                }
            }
        } catch (e) {
            p.fetchError = String(e);
        } finally {
            p.fetchingModels = false;
        }
    }

    function addModelToProvider(idx: number) {
        providers[idx].models.push('');
    }

    function removeModelFromProvider(idx: number, modelIdx: number) {
        providers[idx].models.splice(modelIdx, 1);
    }

    // -- Connection test --
    async function testConnection() {
        testing = true;
        testResult = '';
        try {
            const result = await testConnectionApi(serverUrl.trim(), modelName.trim());
            testResult = result.message;
        } catch (e) {
            testResult = 'Connection failed: ' + String(e);
        } finally {
            testing = false;
        }
    }

    async function handleSave() {
        loading = true;
        error = '';
        try {
            const limits: Record<string, number> = {};
            for (const el of endpointLimits) {
                if (el.url.trim()) limits[el.url.trim()] = el.max;
            }
            const savedProviders: Provider[] = providers
                .filter(p => p.name.trim() || p.url.trim())
                .map(p => ({
                    name: p.name.trim(),
                    display_name: p.display_name.trim(),
                    url: p.url.trim(),
                    models: p.models.filter(m => m.trim()).map(m => m.trim()),
                }));
            const cfg = await updateConfig({
                llama_server_url: serverUrl.trim(),
                model_name: modelName.trim(),
                workspace_dir: workspaceDir.trim(),
                endpoint_limits: limits,
                providers: savedProviders,
                default_provider: defaultProvider.trim(),
            });
            configStore.set(cfg);
            onSaved();
        } catch (e) {
            error = String(e);
        } finally {
            loading = false;
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
            <!-- Default Provider -->
            {#if providers.length > 0}
                <label>
                    Default Provider
                    <select bind:value={defaultProvider}>
                        <option value="">— None (use legacy fields) —</option>
                        {#each providers as p}
                            <option value={p.name}>{p.display_name || p.name}</option>
                        {/each}
                    </select>
                </label>
            {/if}

            <!-- Providers Section -->
            <div class="providers-section">
                <div class="section-header">
                    <label style="margin:0">Providers</label>
                    <button type="button" class="btn-small" onclick={addProvider}>+ Add Provider</button>
                </div>
                {#each providers as p, i}
                    <div class="provider-card">
                        <div class="provider-fields">
                            <input type="text" bind:value={p.name} placeholder="Name (e.g., neptune)" class="field-name" />
                            <input type="text" bind:value={p.display_name} placeholder="Display Name (e.g., Neptune)" />
                            <div class="url-row">
                                <input type="text" bind:value={p.url} placeholder="http://localhost:8080/v1" style="flex:1" />
                                <button type="button" class="btn-fetch" onclick={() => fetchModelsForProvider(i)} disabled={p.fetchingModels || !p.url.trim()}>
                                    {p.fetchingModels ? 'Fetching...' : 'Fetch Models'}
                                </button>
                            </div>
                            {#if p.fetchError}
                                <div class="fetch-error">{p.fetchError}</div>
                            {/if}
                            <div class="models-section">
                                <div class="models-header">
                                    <span class="models-label">Models ({p.models.length})</span>
                                    <button type="button" class="btn-add-model" onclick={() => addModelToProvider(i)}>+ Add</button>
                                </div>
                                {#each p.models as model, mi}
                                    <div class="model-row">
                                        <input type="text" bind:value={p.models[mi]} placeholder="Model name" />
                                        <button type="button" class="btn-remove-model" onclick={() => removeModelFromProvider(i, mi)}>✕</button>
                                    </div>
                                {/each}
                            </div>
                        </div>
                        <button type="button" class="btn-remove-provider" onclick={() => removeProvider(i)} title="Remove provider">✕</button>
                    </div>
                {/each}
                {#if providers.length === 0}
                    <div class="providers-empty">No providers configured. Add one above or use legacy fields below.</div>
                {/if}
            </div>

            <!-- Legacy Fields -->
            <div class="legacy-section">
                <div class="section-header">
                    <label style="margin:0">Legacy (fallback)</label>
                </div>
                <label>
                    Llama.cpp Server URL
                    <input type="text" bind:value={serverUrl} placeholder="http://localhost:8081/v1" />
                </label>
                <label>
                    Model Name
                    <input type="text" bind:value={modelName} placeholder="Leave empty for default" />
                </label>
            </div>

            <label>
                Workspace Directory
                <input type="text" bind:value={workspaceDir} placeholder="./workspace" />
            </label>

            <div class="endpoint-limits">
                <div class="section-header">
                    <label style="margin:0">Endpoint Concurrency Limits</label>
                    <button type="button" class="btn-small" onclick={addEndpointLimit}>+ Add</button>
                </div>
                {#each endpointLimits as el, i}
                    <div class="limit-row">
                        <input type="text" bind:value={el.url} placeholder="Endpoint URL" style="flex:1" />
                        <input type="number" bind:value={el.max} min="1" max="10" style="width:60px" />
                        <button type="button" class="btn-remove" onclick={() => removeEndpointLimit(i)}>✕</button>
                    </div>
                {/each}
            </div>

            <div class="test-section">
                <button type="button" class="btn-test" onclick={testConnection} disabled={testing}>
                    {testing ? 'Testing...' : 'Test Connection'}
                </button>
                {#if testResult}
                    <div class="test-result" class:success={testResult.includes('successful')} class:fail={!testResult.includes('successful')}>
                        {testResult}
                    </div>
                {/if}
            </div>

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

    /* Legacy section */
    .legacy-section {
        display: flex;
        flex-direction: column;
        gap: 8px;
        padding: 12px;
        background: var(--color-surface-0);
        border: 1px dashed var(--color-border);
        border-radius: var(--radius-md);
    }

    /* Endpoint limits */
    .endpoint-limits {
        display: flex;
        flex-direction: column;
        gap: 8px;
    }
    .limit-row {
        display: flex;
        gap: 8px;
        align-items: center;
    }
    .btn-remove {
        color: var(--color-status-red);
        padding: 4px 8px;
    }

    /* Test section */
    .test-section {
        display: flex;
        flex-direction: column;
        gap: 8px;
    }
    .btn-test {
        padding: 8px 16px;
        border-radius: var(--radius-md);
        background: var(--color-surface-3);
        align-self: flex-start;
        cursor: pointer;
    }
    .btn-test:disabled {
        opacity: 0.5;
    }
    .test-result {
        font-size: 13px;
        padding: 8px;
        border-radius: var(--radius-md);
    }
    .test-result.success {
        background: var(--color-status-green)22;
        color: var(--color-status-green);
    }
    .test-result.fail {
        background: var(--color-status-red)22;
        color: var(--color-status-red);
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
        .btn-test,
        .btn-small,
        .btn-fetch,
        .btn-add-model {
            min-height: 44px;
        }
    }
</style>
