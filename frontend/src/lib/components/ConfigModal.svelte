<script lang="ts">
    import type { Config } from '$lib/types';
    import { updateConfig, getConfig } from '$lib/api';
    import { config as configStore, showConfig } from '$lib/store';

    let { config, onClose, onSaved }: { config: Config | null; onClose: () => void; onSaved: () => void } = $props();

    let serverUrl = $state(config?.llama_server_url || '');
    let modelName = $state(config?.model_name || '');
    let workspaceDir = $state(config?.workspace_dir || '');
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

    async function testConnection() {
        testing = true;
        testResult = '';
        try {
            const res = await fetch(serverUrl.replace(/\/$/, '') + '/chat/completions', {
                method: 'POST',
                headers: { 'Content-Type': 'application/json' },
                body: JSON.stringify({ model: 'test', messages: [{ role: 'user', content: 'hi' }], max_tokens: 1 }),
            });
            if (res.ok || res.status === 400) {
                testResult = 'Connection successful!';
            } else {
                testResult = `Connection failed: HTTP ${res.status}`;
            }
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
            const cfg = await updateConfig({
                llama_server_url: serverUrl.trim(),
                model_name: modelName.trim(),
                workspace_dir: workspaceDir.trim(),
                endpoint_limits: limits,
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
            <label>
                Llama.cpp Server URL
                <input type="text" bind:value={serverUrl} placeholder="http://localhost:8081/v1" />
            </label>
            <label>
                Model Name
                <input type="text" bind:value={modelName} placeholder="Leave empty for default" />
            </label>
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
        width: 520px;
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
    input[type="text"], input[type="number"] {
        background: var(--color-surface-2);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-md);
        padding: 8px 12px;
        color: var(--color-text-primary);
        font-size: 14px;
        outline: none;
    }
    input:focus {
        border-color: var(--color-accent);
    }
    .endpoint-limits {
        display: flex;
        flex-direction: column;
        gap: 8px;
    }
    .section-header {
        display: flex;
        align-items: center;
        justify-content: space-between;
    }
    .btn-small {
        padding: 4px 10px;
        border-radius: var(--radius-sm);
        background: var(--color-surface-3);
        font-size: 12px;
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
    }
    .btn-primary:disabled {
        opacity: 0.5;
        cursor: not-allowed;
    }
    button[type="button"] {
        padding: 8px 16px;
        border-radius: var(--radius-md);
        background: var(--color-surface-3);
    }
</style>