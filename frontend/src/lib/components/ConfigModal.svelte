<script lang="ts">
    import { onMount } from 'svelte';
    import type { Config, Provider } from '$lib/types';
    import { isAgentProvider, PI_SENTINEL_URL } from '$lib/types';
    import { updateConfig, getConfig, fetchModels as fetchModelsApi, testConnection as testConnectionApi, testPiConnection as testPiConnectionApi } from '$lib/api';
    import { config as configStore, selectedProject, pwaInstallPrompt } from '$lib/stores/app';
    import { showConfig } from '$lib/stores/ui';
    import { createFocusTrap } from '$lib/a11y';
    import { REASONING_EFFORT_OPTIONS } from '$lib/types';
    import ToggleSwitch from './ToggleSwitch.svelte';
    import TaskDefaults from './ConfigModal/TaskDefaults.svelte';
    import ProviderCard from './ConfigModal/ProviderCard.svelte';

    let { config, onClose, onSaved, onSignOut }: { config: Config | null; onClose: () => void; onSaved: () => void; onSignOut?: () => void | Promise<void> } = $props();

    const MODEL_COLORS = [
        '#6E7681', '#4C9AFF', '#3FB950', '#D29922', '#F85149',
        '#A371F7', '#E3B341', '#79C0FF', '#7EE787', '#FF7B72',
        '#D2A8FF', '#F0883E', '#56B6C2', '#F778BA', '#DB6D28',
    ];
    function getModelColor(modelName: string): string {
        let hash = 0;
        for (let i = 0; i < modelName.length; i++) {
            hash = ((hash << 5) - hash + modelName.charCodeAt(i)) | 0;
        }
        return MODEL_COLORS[Math.abs(hash) % MODEL_COLORS.length];
    }

    let defaultProvider = $state('');
    let defaultAutoCommit = $state(false);
    let defaultAutoMerge = $state(true);
    let defaultSubtasksAutoMerge = $state(true);
    let defaultPlanMode = $state(false);
    let defaultMaxFailedTurns = $state(3);
    let loading = $state(false);
    let error = $state('');

    // Locale-aware currency formatting for pricing inputs
    const currencySymbol = (() => {
        const parts = new Intl.NumberFormat(undefined, { style: 'currency', currency: 'USD', minimumFractionDigits: 0, maximumFractionDigits: 0 }).formatToParts(0);
        const currencyPart = parts.find(p => p.type === 'currency');
        return currencyPart ? currencyPart.value : '$';
    })();

    // -- Provider management --
    interface ModelEntry {
        name: string;
        readonly: boolean; // true = fetched from API, not editable
        reasoning_effort?: 'off' | 'low' | 'medium' | 'high' | 'max';
        context_window?: number;
    }

    interface ProviderDraft {
        name: string;
        display_name: string;
        kind: 'llm' | 'agent';
        url: string;
        api_key: string; // full value when editing, masked ("••••••••••") when loaded from config
        agent_config_dir?: string;
        models: ModelEntry[];
        concurrency: number;
        default_model: string; // per-provider default model
        // Pricing (USD per 1M tokens)
        input_price: number;
        output_price: number;
        cache_read_price: number;
        cache_write_price: number;
        // Power & electricity
        power_consumption_w: number;
        electricity_cost_per_kwh: number;
        fetchingModels: boolean;
        fetchError: string;
        expanded: boolean; // card expansion state
        testingConnection: boolean;
        testResult: { success: boolean; message: string } | null;
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

    let providers = $state<ProviderDraft[]>([]);
    let encryptionEnabled = $state(false);

    function buildProviders(cfg: Config | null): ProviderDraft[] {
        const oldGlobalDefault = '';
        return (cfg?.providers || []).map(p => {
            // Detect Pi provider by kind field OR sentinel URL
            const isAgent = p.kind === 'agent' || p.url === PI_SENTINEL_URL;
            const draft: ProviderDraft = {
                ...p,
                kind: isAgent ? 'agent' : (p.kind || 'llm'),
                api_key: p.api_key || '',
                concurrency: p.concurrency || 1,
                default_model: p.default_model || '',
                input_price: p.input_price || 0,
                output_price: p.output_price || 0,
                cache_read_price: p.cache_read_price || 0,
                cache_write_price: p.cache_write_price || 0,
                power_consumption_w: p.power_consumption_w || 0,
                electricity_cost_per_kwh: p.electricity_cost_per_kwh || 0,
                fetchingModels: false,
                fetchError: '',
                expanded: false,
                testingConnection: false,
                testResult: null,
                models: isAgent ? [] : (p.models || []).map(m => {
                    // Handle both old format (string) and new format ({name, reasoning_effort})
                    if (typeof m === 'string') {
                        return { name: m, readonly: false, reasoning_effort: 'medium' as const };
                    }
                    return { name: m.name, readonly: false, reasoning_effort: m.reasoning_effort || 'medium' };
                }).sort((a, b) => a.name.localeCompare(b.name, undefined, { sensitivity: 'base' })),
            };
            // Migrate: if old global default_model belongs to this provider and provider has no default set
            if (!isAgent && !draft.default_model && oldGlobalDefault && p.models?.some(m => (typeof m === 'string' ? m : m.name) === oldGlobalDefault)) {
                draft.default_model = oldGlobalDefault;
            }
            return draft;
        });
    }

    // Sync form fields and providers from config on mount and when config changes
    $effect(() => {
        defaultProvider = config?.default_provider || '';
        defaultAutoCommit = config?.default_auto_commit ?? false;
        defaultAutoMerge = config?.default_auto_merge ?? true;
        defaultSubtasksAutoMerge = config?.default_subtasks_auto_merge ?? true;
        defaultPlanMode = config?.default_plan_mode ?? false;
        defaultMaxFailedTurns = config?.default_max_failed_turns ?? 3;
        encryptionEnabled = config?.encryption_enabled ?? false;
        providers = buildProviders(config);
    });

    // Close agent menu when clicking outside
    $effect(() => {
        if (!agentMenuOpen) return;
        function handleClick(e: MouseEvent) {
            if (agentMenuBtn && agentMenuBtn.contains(e.target as Node)) return;
            const menu = document.querySelector('.agent-menu');
            if (menu && menu.contains(e.target as Node)) return;
            agentMenuOpen = false;
            agentMenuRect = null;
        }
        document.addEventListener('mousedown', handleClick);
        return () => document.removeEventListener('mousedown', handleClick);
    });

    function toggleProvider(idx: number) {
        providers[idx].expanded = !providers[idx].expanded;
    }

    async function testProviderConnection(idx: number) {
        const p = providers[idx];
        p.testingConnection = true;
        p.testResult = null;
        try {
            if (p.kind === 'agent') {
                // Test Pi connection (runs `pi --version`)
                const result = await testPiConnectionApi();
                p.testResult = {
                    success: result.success,
                    message: result.success ? `Pi available: ${result.message}` : result.message,
                };
            } else {
                if (!p.url.trim()) { p.testingConnection = false; return; }
                const defaultModelName = p.default_model ? parseDefaultModel(p.default_model).name : (p.models.find(m => m.name)?.name || '');
                const apiKey = p.api_key || '';
                const result = await testConnectionApi(p.url.trim(), defaultModelName, apiKey);
                p.testResult = {
                    success: result.success,
                    message: result.message,
                };
            }
        } catch (e) {
            p.testResult = { success: false, message: String(e) };
        } finally {
            p.testingConnection = false;
        }
    }

    function addProvider() {
        providers.push({
            name: '',
            display_name: '',
            kind: 'llm',
            url: '',
            api_key: '',
            models: [],
            concurrency: 1,
            default_model: '',
            input_price: 0,
            output_price: 0,
            cache_read_price: 0,
            cache_write_price: 0,
            power_consumption_w: 0,
            electricity_cost_per_kwh: 0,
            fetchingModels: false,
            fetchError: '',
            expanded: true,
            testingConnection: false,
            testResult: null,
        });
    }

    let agentMenuOpen = $state(false);
    let agentMenuBtn: HTMLButtonElement | null = $state(null);
    let agentMenuRect = $state<{ top: number; left: number } | null>(null);

    function toggleAgentMenu(e: MouseEvent) {
        e.stopPropagation();
        if (providers.some(p => p.kind === 'agent')) return;
        if (agentMenuOpen) {
            agentMenuOpen = false;
            agentMenuRect = null;
        } else {
            if (agentMenuBtn) {
                const r = agentMenuBtn.getBoundingClientRect();
                agentMenuRect = { top: r.bottom + 4, left: r.left };
            }
            agentMenuOpen = true;
        }
    }

    function addAgent() {
        // Only one Pi agent allowed
        if (providers.some(p => p.kind === 'agent')) {
            return;
        }
        agentMenuOpen = false;
        agentMenuRect = null;
        providers.push({
            name: 'pi',
            display_name: 'Pi Coding Agent',
            kind: 'agent',
            url: PI_SENTINEL_URL,
            agent_config_dir: '~/.pi/agent',
            api_key: '',
            models: [],
            concurrency: 1,
            default_model: '',
            input_price: 0,
            output_price: 0,
            cache_read_price: 0,
            cache_write_price: 0,
            power_consumption_w: 0,
            electricity_cost_per_kwh: 0,
            fetchingModels: false,
            fetchError: '',
            expanded: true,
            testingConnection: false,
            testResult: null,
        });
    }

    function removeProvider(idx: number) {
        providers.splice(idx, 1);
    }

    function swapProviders(fromIdx: number, toIdx: number) {
        if (fromIdx === toIdx || toIdx < 0 || toIdx >= providers.length) return;
        const [item] = providers.splice(fromIdx, 1);
        providers.splice(toIdx, 0, item);
    }

    // -- Drag-and-drop reordering --
    let dragIndex = $state(-1);
    let dragDropIndex = $state(-1);
    let dragPreview: HTMLElement | null = null;
    /** Find the provider card index under the given pointer coordinates. */
    function getCardIndexAt(x: number, y: number): number {
        const el = document.elementFromPoint(x, y);
        if (!el) return -1;
        const card = el.closest('.provider-card');
        if (!card) return -1;
        return parseInt(card.getAttribute('data-index') || '-1', 10);
    }

    function handleDragStart(e: PointerEvent, idx: number) {
        e.preventDefault();
        e.stopPropagation();
        if (providers.length < 2) return;

        dragIndex = idx;
        dragDropIndex = -1;

        // Create drag preview element
        dragPreview = document.createElement('div');
        dragPreview.className = 'drag-preview';
        dragPreview.style.left = `${e.clientX - 12}px`;
        dragPreview.style.top = `${e.clientY - 12}px`;
        document.body.appendChild(dragPreview);

        const startY = e.clientY;
        const startIdx = idx;

        function onPointerMove(ev: PointerEvent) {
            ev.preventDefault();
            // Update preview position
            if (dragPreview) {
                dragPreview.style.left = `${ev.clientX - 12}px`;
                dragPreview.style.top = `${ev.clientY - 12}px`;
            }
            // Find drop target
            const targetIdx = getCardIndexAt(ev.clientX, ev.clientY);
            if (targetIdx >= 0 && targetIdx !== dragIndex && targetIdx !== dragDropIndex) {
                // Adjust index if dragging downwards and target is the old position
                let adjustedIdx = targetIdx;
                if (dragIndex < targetIdx && targetIdx !== startIdx) {
                    // target is below drag source — no adjustment needed
                }
                dragDropIndex = adjustedIdx;
                swapProviders(dragIndex, adjustedIdx);
                dragIndex = adjustedIdx;
            }
        }

        function onPointerUp(ev: PointerEvent) {
            ev.preventDefault();
            document.removeEventListener('pointermove', onPointerMove as any);
            document.removeEventListener('pointerup', onPointerUp as any);

            // Remove drag preview
            if (dragPreview) {
                dragPreview.remove();
                dragPreview = null;
            }
            dragIndex = -1;
            dragDropIndex = -1;
        }

        document.addEventListener('pointermove', onPointerMove, { passive: false });
        document.addEventListener('pointerup', onPointerUp);
    }

    async function fetchModelsForProvider(idx: number) {
        const p = providers[idx];
        if (!p.url.trim()) return;
        p.fetchingModels = true;
        p.fetchError = '';
        try {
            const apiKey = p.api_key || '';
            const result = await fetchModelsApi(p.url.trim(), apiKey);
            if (result.error) {
                p.fetchError = result.error;
            } else {
                // Replace: clear existing models and use fetched list (readonly)
                p.models = result.models.map(m => ({ name: m.name, readonly: true, reasoning_effort: 'medium' as const, context_window: m.context_window }));
                p.models.sort((a, b) => a.name.localeCompare(b.name, undefined, { sensitivity: 'base' }));
            }
        } catch (e) {
            p.fetchError = String(e);
        } finally {
            p.fetchingModels = false;
        }
    }

    function addModelToProvider(idx: number) {
        providers[idx].models.push({ name: '', readonly: false, reasoning_effort: 'medium' });
        providers[idx].models.sort((a, b) => a.name.localeCompare(b.name, undefined, { sensitivity: 'base' }));
    }

    function removeModelFromProvider(idx: number, modelIdx: number) {
        const p = providers[idx];
        const removed = p.models[modelIdx];
        const removedName = removed?.name || '';
        const removedEffort = removed?.reasoning_effort || 'medium';
        const removedCompositeKey = removedEffort !== 'medium' ? `${removedName}|${removedEffort}` : removedName;
        p.models.splice(modelIdx, 1);
        // Clear default if the removed model+effort pair was the default
        if (p.default_model === removedCompositeKey) {
            p.default_model = '';
        }
    }

    function setProviderDefaultModel(idx: number, modelName: string, reasoningEffort?: string) {
        const p = providers[idx];
        const compositeKey = reasoningEffort && reasoningEffort !== 'medium' ? `${modelName}|${reasoningEffort}` : modelName;
        if (p.default_model === compositeKey) {
            p.default_model = ''; // toggle off
        } else {
            p.default_model = compositeKey;
        }
    }

    /** Parse a default_model value (possibly modelName|effort) into {name, effort}. */
    function parseDefaultModel(value: string): { name: string; effort: string } {
        if (!value) return { name: '', effort: '' };
        const parts = value.split('|');
        return { name: parts[0], effort: parts[1] || 'medium' };
    }

    function toggleDefaultProvider(providerName: string) {
        if (defaultProvider === providerName) {
            defaultProvider = '';
        } else {
            defaultProvider = providerName;
            // Move default provider to top
            const idx = providers.findIndex(p => p.name === providerName);
            if (idx > 0) {
                const [item] = providers.splice(idx, 1);
                providers.unshift(item);
            }
        }
    }

    async function handleSave() {
        loading = true;
        error = '';
        try {
            const savedProviders: Provider[] = providers
                .filter(p => p.kind === 'agent' || p.url.trim())
                .map(p => {
                    if (p.kind !== 'agent') syncProviderMeta(p);
                    const trimmedKey = p.api_key?.trim() || '';
                    // Masked keys are all bullets — don't send them. Only send real values.
                    const isMasked = trimmedKey && trimmedKey.replace(/•/g, '').length === 0;
                    const base: Provider = {
                        name: p.name.trim(),
                        display_name: p.display_name.trim(),
                        kind: p.kind,
                        url: p.url.trim(),
                        models: p.models.filter(m => m.name.trim()).map(m => ({
                            name: m.name.trim(),
                            reasoning_effort: m.reasoning_effort || 'medium',
                        })),
                        concurrency: Math.max(1, p.concurrency || 1),
                        default_model: p.default_model || '',
                        input_price: p.input_price,
                        output_price: p.output_price,
                        cache_read_price: p.cache_read_price,
                        cache_write_price: p.cache_write_price,
                        power_consumption_w: p.power_consumption_w,
                        electricity_cost_per_kwh: p.electricity_cost_per_kwh,
                    };
                    if (!isMasked && p.kind !== 'agent') {
                        base.api_key = trimmedKey;
                    }
                    return base;
                });
            const cfg = await updateConfig({
                workspace_dir: config?.workspace_dir || '',
                providers: savedProviders,
                default_provider: defaultProvider,
                default_auto_commit: defaultAutoCommit,
                default_auto_merge: defaultAutoMerge,
                default_subtasks_auto_merge: defaultSubtasksAutoMerge,
                default_plan_mode: defaultPlanMode,
                default_max_failed_turns: defaultMaxFailedTurns,
            } as Config);
            configStore.set(cfg);
            onSaved();
        } catch (e) {
            error = String(e);
        } finally {
            loading = false;
        }
    }

    let modalEl: HTMLDivElement | null = null;
    let focusTrap: ReturnType<typeof createFocusTrap> | null = null;
    let triggerEl: HTMLElement | null = null;

    onMount(() => {
        // Store the element that triggered this modal for focus restoration
        triggerEl = document.activeElement as HTMLElement;
        // Setup focus trap after modal is rendered
        if (modalEl) {
            focusTrap = createFocusTrap(modalEl, handleClose);
            // Focus the close button or first interactive element
            setTimeout(() => focusTrap?.focusFirst(), 50);
        }
    });

    function handleClose() {
        focusTrap?.destroy();
        focusTrap = null;
        onClose();
        // Restore focus to trigger element
        setTimeout(() => triggerEl?.focus(), 50);
    }

    // PWA install handler
    async function handlePwaInstall() {
        const promptEvent = $pwaInstallPrompt;
        if (!promptEvent) return;
        // Fire the install prompt
        (promptEvent as any).prompt?.();
        // Wait for the user to accept or dismiss
        const { outcome } = await (promptEvent as any).userChoice || { outcome: 'dismissed' };
        if (outcome === 'dismissed') {
            // User dismissed — clear the prompt so the button goes away
            pwaInstallPrompt.set(null);
        }
    }
</script>

<div class="overlay" onclick={handleClose} role="presentation">
    <div class="modal" onclick={(e) => e.stopPropagation()} onkeydown={(e) => { if (e.key === 'Escape') handleClose(); }} bind:this={modalEl} role="dialog" aria-modal="true" aria-labelledby="config-modal-title" tabindex="-1">
        <div class="modal-header">
            <h2 id="config-modal-title">Settings</h2>
            <button class="close-btn" onclick={handleClose} aria-label="Close settings" title="Close settings">✕</button>
        </div>
        <form class="modal-body" onsubmit={(e) => { e.preventDefault(); handleSave(); }}>
            <!-- Providers Section -->
            <fieldset class="section">
                <div class="section-header">
                    <h3 class="section-label">Providers</h3>
                    <div class="section-actions">
                        {#if !providers.some(p => p.kind === 'agent')}
                            <button type="button" class="btn-small btn-agent" onclick={toggleAgentMenu} bind:this={agentMenuBtn} title="Add a coding agent" class:open={agentMenuOpen}>
                                + Add Agent
                                <svg class="chevron" width="14" height="14" viewBox="0 0 16 16" fill="none"><path d="M4 6l4 4 4-4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>
                            </button>
                        {/if}
                        <button type="button" class="btn-small" onclick={addProvider} title="Add a new LLM provider">+ Add Provider</button>
                    </div>
                    <!-- Agent selection dropdown -->
                    {#if agentMenuOpen && agentMenuRect}
                        <div class="agent-menu" style="position:fixed;top:{agentMenuRect.top}px;left:{agentMenuRect.left}px;z-index:10001" onclick={(e) => e.stopPropagation()} onkeydown={(e) => { if (e.key === 'Escape') agentMenuOpen = false; }} role="menu" tabindex="-1">
                            <button type="button" class="agent-menu-item" onclick={addAgent}>
                                <svg width="14" height="14" viewBox="0 0 16 16" fill="none">
                                    <rect x="3" y="5" width="10" height="8" rx="1.5" stroke="currentColor" stroke-width="1.5"/>
                                    <circle cx="6" cy="9" r="1" fill="currentColor"/>
                                    <circle cx="10" cy="9" r="1" fill="currentColor"/>
                                </svg>
                                Pi Coding Agent
                            </button>
                        </div>
                    {/if}
                </div>
                <div class="providers-list">
                    {#each providers as p, i (p.name)}
                        <ProviderCard
                            provider={p}
                            index={i}
                            {defaultProvider}
                            {encryptionEnabled}
                            {currencySymbol}
                            {getModelColor}
                            {parseDefaultModel}
                            isDragging={dragIndex === i}
                            onDragStart={(e) => handleDragStart(e, i)}
                            onRemove={() => removeProvider(i)}
                            onFetchModels={() => fetchModelsForProvider(i)}
                            onTestConnection={() => testProviderConnection(i)}
                            onToggleDefaultProvider={() => toggleDefaultProvider(p.name)}
                            onAddModel={() => addModelToProvider(i)}
                            onRemoveModel={(mi) => removeModelFromProvider(i, mi)}
                            onSetDefaultModel={(name, effort) => setProviderDefaultModel(i, name, effort)}
                            onSyncMeta={() => syncProviderMeta(p)}
                        />
                    {/each}
                </div>
                {#if providers.length > 0 && !providers.some(p => p.kind === 'llm')}
                    <div class="no-llm-warning">
                        <svg width="14" height="14" viewBox="0 0 16 16" fill="none">
                            <path d="M8 1L1 14h14L8 1Z" stroke="currentColor" stroke-width="1.3" stroke-linejoin="round"/>
                            <path d="M8 6v4M8 11.5v.5" stroke="currentColor" stroke-width="1.3" stroke-linecap="round"/>
                        </svg>
                        <span>No LLM provider configured. An LLM provider is required for <strong>commit label generation</strong> and <strong>automatic merge conflict resolution</strong>.</span>
                    </div>
                {/if}
                {#if providers.length === 0}
                    <div class="providers-empty">
                        <svg width="40" height="40" viewBox="0 0 40 40" fill="none" class="empty-icon">
                            <rect x="4" y="8" width="32" height="24" rx="3" stroke="currentColor" stroke-width="1.5"/>
                            <path d="M4 16h32" stroke="currentColor" stroke-width="1.5"/>
                            <circle cx="10" cy="12" r="1" fill="currentColor"/>
                            <circle cx="15" cy="12" r="1" fill="currentColor"/>
                            <circle cx="20" cy="12" r="1" fill="currentColor"/>
                        </svg>
                        <p>No providers configured</p>
                        <span>Add an LLM provider to get started. Supports any OpenAI-compatible API.</span>
                    </div>
                {/if}
            </fieldset>

            <!-- General Section -->
            <fieldset class="section">
                <h3 class="section-label">General</h3>
                <div class="field-label">
                    <span class="field-label-inline">Directory for Worktrees</span>
                    <div class="readonly-value">{config?.workspace_dir || '—'}</div>
                    <span class="field-help">Directory where task worktrees are created</span>
                </div>
                <div class="field-label">
                    <span class="field-label-inline">Database</span>
                    <div class="readonly-value">{config?.database_path || '—'}</div>
                    <span class="field-help">Path to the server's SQLite database</span>
                </div>
                {#if $pwaInstallPrompt}
                    <button type="button" class="btn-pwa-install" onclick={handlePwaInstall} title="Install as a Progressive Web App">
                        <svg width="14" height="14" viewBox="0 0 16 16" fill="none">
                            <path d="M8 2v9M5 8l3 3 3-3M3 12v2h10v-2" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                        </svg>
                        Install as PWA
                    </button>
                {/if}
            </fieldset>

            <!-- Task Defaults Section -->
            <TaskDefaults
                bind:defaultPlanMode
                bind:defaultAutoMerge
                bind:defaultSubtasksAutoMerge
                bind:defaultMaxFailedTurns
                originBranch={$selectedProject?.origin_branch || 'main'}
            />

            {#if error}
                <div id="config-error" class="error" role="alert" aria-live="assertive">{error}</div>
            {/if}

            <div class="modal-footer">
                {#if onSignOut}
                    <button type="button" class="btn-signout" onclick={onSignOut} title="Sign out of Kanban">Sign out</button>
                {/if}
                <div class="modal-footer-right">
                    <button type="button" onclick={handleClose} title="Cancel and discard changes">Cancel</button>
                    <button type="submit" class="btn-primary" disabled={loading} title="Save settings">
                        {loading ? 'Saving...' : 'Save'}
                    </button>
                </div>
            </div>
        </form>
    </div>
</div>

<style>
    @import '$lib/styles/shared.css';

    :global(.overlay) {
        z-index: 100;
    }
    .modal-body {
        padding: 20px 20px 0;
        display: flex;
        flex-direction: column;
        gap: 24px;
        overflow-y: auto;
        flex: 1;
        min-height: 0;
        scroll-behavior: smooth;
    }
    .modal-body::-webkit-scrollbar {
        width: 6px;
    }
    .modal-body::-webkit-scrollbar-track {
        background: transparent;
    }
    .modal-body::-webkit-scrollbar-thumb {
        background: var(--color-surface-4);
        border-radius: 3px;
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
    .section-label {
        margin: 0;
        font-size: 0.875rem;
        font-weight: 600;
        text-transform: uppercase;
        letter-spacing: 0.05em;
        color: var(--color-text-secondary);
    }
    .section-header {
        display: flex;
        align-items: center;
        justify-content: space-between;
    }
    .section-actions {
        display: flex;
        gap: 8px;
    }
    .btn-agent {
        display: inline-flex;
        align-items: center;
        gap: 8px;
        line-height: 1;
        color: #A371F7;
        border-color: #A371F733;
    }
    .btn-agent:hover {
        background: #A371F711;
    }
    .btn-agent .chevron {
        transition: transform 0.15s ease;
        flex-shrink: 0;
    }
    .btn-agent.open .chevron {
        transform: rotate(180deg);
    }

    /* Agent selection dropdown */
    .agent-menu {
        min-width: 200px;
        background: var(--color-surface-1, #1e1e2e);
        border: 1px solid var(--color-border, #333);
        border-radius: var(--radius-md, 8px);
        box-shadow: 0 8px 24px rgba(0, 0, 0, 0.2);
        padding: 4px;
    }
    .agent-menu-item {
        display: flex;
        align-items: center;
        gap: 8px;
        width: 100%;
        padding: 8px 10px;
        border-radius: var(--radius-sm, 4px);
        border: none;
        background: none;
        color: #A371F7;
        font-size: 0.8125rem;
        font-family: inherit;
        font-weight: 500;
        cursor: pointer;
        text-align: left;
        transition: background 0.1s ease;
    }
    .agent-menu-item:hover {
        background: rgba(163, 113, 247, 0.08);
    }

    /* Field labels */
    .field-label {
        display: flex;
        flex-direction: column;
        gap: 6px;
        font-size: 0.875rem;
        font-weight: 500;
        color: var(--color-text-primary);
    }
    .field-label input {
        width: 100%;
    }
    .field-help {
        font-size: 0.75rem;
        color: var(--color-text-tertiary);
        font-weight: 400;
    }
    .readonly-value {
        font-family: var(--font-mono, 'SF Mono', 'Cascadia Code', 'Fira Code', monospace);
        font-size: 0.8125rem;
        background: var(--color-surface-2);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-md);
        padding: 8px 12px;
        color: var(--color-text-secondary);
        user-select: all;
    }
    .field-label-inline {
        font-size: 0.875rem;
        font-weight: 500;
        color: var(--color-text-primary);
    }

    /* Inputs */
    input[type="text"], input[type="password"], input[type="number"], select {
        background: var(--color-surface-2);
        border: 1px solid var(--color-border-bright);
        border-radius: var(--radius-md);
        padding: 8px 12px;
        color: var(--color-text-primary);
        font-size: 0.875rem;
    }
    input:focus-visible, select:focus-visible {
        border-color: var(--color-accent);
        outline: none;
    }

    /* Buttons */
    .btn-small {
        padding: 6px 12px;
        border-radius: var(--radius-sm);
        background: var(--color-surface-3);
        color: var(--color-text-primary);
        font-size: 0.875rem;
        font-weight: 500;
        cursor: pointer;
        white-space: nowrap;
    }
    .btn-small:hover {
        background: var(--color-surface-4);
    }

    /* Providers list container */
    .providers-list {
        display: flex;
        flex-direction: column;
        gap: 8px;
    }
    .providers-empty {
        display: flex;
        flex-direction: column;
        align-items: center;
        gap: 8px;
        padding: 32px 16px;
        text-align: center;
        color: var(--color-text-secondary);
    }
    .providers-empty p {
        font-size: 0.875rem;
        font-weight: 500;
        color: var(--color-text-primary);
    }
    .providers-empty span {
        font-size: 0.875rem;
        max-width: 280px;
    }
    .empty-icon {
        color: var(--color-text-tertiary);
        margin-bottom: 4px;
    }
    .no-llm-warning {
        display: flex;
        align-items: flex-start;
        gap: 6px;
        margin-top: 10px;
        padding: 8px 10px;
        background: rgba(210, 153, 34, 0.08);
        border: 1px solid rgba(210, 153, 34, 0.25);
        border-radius: var(--radius-sm);
        font-size: 0.8rem;
        color: #d29922;
        line-height: 1.4;
    }
    .no-llm-warning svg {
        flex-shrink: 0;
        margin-top: 1px;
    }
    .no-llm-warning strong {
        color: #e3b341;
    }

    .error {
        color: var(--color-status-red);
        font-size: 0.875rem;
        font-weight: 500;
    }
    .modal-footer {
        display: flex;
        align-items: center;
        justify-content: space-between;
        gap: 8px;
        padding: 12px 20px;
        border-top: 1px solid var(--color-border);
        background: var(--color-surface-1);
        flex-shrink: 0;
        position: sticky;
        bottom: 0;
        z-index: 10;
    }
    .modal-footer-right {
        display: flex;
        gap: 8px;
    }
    .btn-signout {
        padding: 8px 16px;
        border-radius: var(--radius-md);
        background: transparent;
        color: var(--color-status-red, #f85149);
        font-size: 0.875rem;
        font-weight: 500;
        cursor: pointer;
        border: 1px solid var(--color-status-red, #f85149);
        transition: background 0.15s, color 0.15s;
    }
    .btn-signout:hover {
        background: var(--color-status-red, #f85149);
        color: white;
    }
    .btn-pwa-install {
        display: inline-flex;
        align-items: center;
        gap: 6px;
        padding: 8px 14px;
        border-radius: var(--radius-md);
        background: var(--color-surface-3);
        color: var(--color-accent);
        font-size: 0.875rem;
        font-weight: 500;
        cursor: pointer;
        border: 1px solid var(--color-accent);
        transition: background 0.15s, color 0.15s;
    }
    .btn-pwa-install:hover {
        background: var(--color-accent);
        color: white;
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
            max-height: 100dvh;
            height: 100dvh;
            border-radius: 0;
            border: none;
        }
        .modal-header {
            padding: 16px;
        }
        .modal-body {
            padding: 16px;
            padding-bottom: calc(56px + env(safe-area-inset-bottom));
        }
        .modal-footer {
            position: fixed;
            bottom: 0;
            left: 0;
            right: 0;
            width: 100vw;
            padding: 8px 16px;
            padding-bottom: calc(4px + env(safe-area-inset-bottom));
        }
    }
</style>
