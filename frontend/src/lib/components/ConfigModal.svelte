<script lang="ts">
    
    import type { Config, Provider, Extension } from '$lib/types';
    import { isAgentProvider, AGENT_URL_PREFIX } from '$lib/types';
    import { updateConfig, getConfig, fetchModels as fetchModelsApi, testConnection as testConnectionApi } from '$lib/api';
    import { listExtensions } from '$lib/api/extensions';
    import { appStore, uiStore, dialogStore } from '$lib/stores';
    import { useIsMobile } from '$lib/composables/use-is-mobile.svelte';
    import BaseModal from './BaseModal.svelte';
    import { REASONING_EFFORT_OPTIONS } from '$lib/types';
    import ToggleSwitch from './ToggleSwitch.svelte';
    import TaskDefaults from './ConfigModal/TaskDefaults.svelte';
    import ProviderCard from './ConfigModal/ProviderCard.svelte';
    import ExtensionsTab from './ConfigModal/ExtensionsTab.svelte';
    import pwaIcon from '$lib/assets/icons/pwa-icon.svg?raw';

    let { config, onClose, onSaved, onSignOut }: { config: Config | null; onClose: () => void; onSaved: () => void; onSignOut?: () => void | Promise<void> } = $props();

    const MODEL_COLORS = [
        '#888888', '#4C9AFF', '#3FB950', '#D29922', '#F85149',
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
    let defaultOperatingMode = $state('act');
    let defaultMaxFailedTurns = $state(3);
    let loading = $state(false);
    let error = $state('');

    // Tab navigation
    let activeTab = $state('providers');

    // Track unsaved changes
    let hasChanges = $state(false);
    function markDirty() {
        hasChanges = true;
    }

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
        uid: string; // stable id for {#each} key — never changes after creation
        name: string;
        display_name: string;
        kind: 'llm' | 'agent';
        url: string;
        api_key: string; // full value when editing, masked ("••••••••••") when loaded from config
        agent_config_dir?: string;
        agent_description?: string; // agent description text (from manifest)
        agent_setup_info?: string;  // agent setup instructions (from manifest)
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
    let _providerUidCounter = 0;

    function buildProviders(cfg: Config | null): ProviderDraft[] {
        const oldGlobalDefault = '';
        return (cfg?.providers || []).map(p => {
            // Detect agent provider by kind field, agent:// prefix, or legacy sentinel URL
            const isAgent = p.kind === 'agent' || p.url.startsWith(AGENT_URL_PREFIX);
            const draft: ProviderDraft = {
                uid: p.name, // use existing name as stable uid for loaded providers
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
        defaultAutoMerge = config?.default_auto_merge ?? false;
        defaultSubtasksAutoMerge = config?.default_subtasks_auto_merge ?? true;
        defaultOperatingMode = config?.default_operating_mode ?? 'act';
        defaultMaxFailedTurns = config?.default_max_failed_turns ?? 3;
        encryptionEnabled = config?.encryption_enabled ?? false;
        providers = buildProviders(config);
        hasChanges = false;
        loadAvailableAgents();
    });

    // Enrich agent providers with manifest data (description, setup_info) when extensions are loaded
    $effect(() => {
        if (availableAgents.length === 0) return;
        for (const p of providers) {
            if (p.kind !== 'agent') continue;
            const agentName = p.url.startsWith(AGENT_URL_PREFIX) ? p.url.slice(AGENT_URL_PREFIX.length) : p.name;
            const ext = availableAgents.find(e => e.name === agentName);
            if (ext?.manifest.agent) {
                p.agent_description = ext.manifest.agent.description || '';
                p.agent_setup_info = ext.manifest.agent.setup_info || '';
            }
        }
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
                // Agent providers don't have a testable connection (binary is checked at runtime)
                p.testResult = { success: true, message: 'Agent provider — binary checked at runtime' };
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
            uid: `new-${++_providerUidCounter}-${Date.now()}`,
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
        markDirty();
    }

    let agentMenuOpen = $state(false);
    let agentMenuBtn: HTMLButtonElement | null = $state(null);
    let agentMenuRect = $state<{ top: number; left: number } | null>(null);
    let availableAgents = $state<Extension[]>([]);
    let agentsLoading = $state(false);

    async function loadAvailableAgents() {
        if (agentsLoading || availableAgents.length > 0) return;
        agentsLoading = true;
        try {
            const all = await listExtensions();
            availableAgents = all.filter(e => e.type === 'agent' && e.enabled);
        } catch {
            availableAgents = [];
        } finally {
            agentsLoading = false;
        }
    }

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
            loadAvailableAgents();
        }
    }

    function addAgent(ext: Extension) {
        if (providers.some(p => p.kind === 'agent')) {
            return;
        }
        agentMenuOpen = false;
        agentMenuRect = null;
        providers.push({
            uid: `agent-${ext.name}`,
            name: ext.name,
            display_name: ext.manifest.display_name || ext.name,
            kind: 'agent',
            url: AGENT_URL_PREFIX + ext.name,
            agent_config_dir: ext.manifest.agent?.config_mount?.host_path || '',
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
        markDirty();
    }

    function removeProvider(idx: number) {
        providers.splice(idx, 1);
        markDirty();
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
                        agent_config_dir: p.agent_config_dir || '',
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
                default_operating_mode: defaultOperatingMode,
                default_max_failed_turns: defaultMaxFailedTurns,
            } as Config);
            appStore.config = cfg;
            onSaved();
        } catch (e) {
            error = String(e);
        } finally {
            loading = false;
        }
    }

    async function handleClose() {
        if (hasChanges) {
            const confirmed = await dialogStore.showConfirm({
                title: 'Discard changes?',
                message: 'You have unsaved changes. Discard them?',
                confirmText: 'Discard',
                cancelText: 'Keep editing',
                variant: 'destructive',
            });
            if (!confirmed) return;
        }
        hasChanges = false;
        onClose();
    }

    async function handleSignOut() {
        const confirmed = await dialogStore.showConfirm({
            title: 'Sign out',
            message: 'Are you sure you want to sign out?',
            confirmText: 'Sign out',
            variant: 'destructive',
        });
        if (confirmed && onSignOut) {
            await onSignOut();
        }
    }

    // Fullscreen toggle
    let modalFullscreen = $state(false);
    const isMobileCtx = useIsMobile();
    function toggleModalFullscreen() {
        modalFullscreen = !modalFullscreen;
    }

    // PWA install handler
    async function handlePwaInstall() {
        const promptEvent = appStore.pwaInstallPrompt;
        if (!promptEvent) return;
        // Fire the install prompt
        (promptEvent as any).prompt?.();
        // Wait for the user to accept or dismiss
        const { outcome } = await (promptEvent as any).userChoice || { outcome: 'dismissed' };
        if (outcome === 'dismissed') {
            // User dismissed — clear the prompt so the button goes away
            appStore.pwaInstallPrompt = null;
        }
    }
</script>

{#snippet customHeader({ title }: { title: string })}
    <div class="header-wrap">
        <h2 id="modal-title">{title}</h2>
        <div class="header-tabs">
            <button type="button" class="tab-btn" class:active={activeTab === 'providers'} onclick={() => activeTab = 'providers'}>Providers</button>
            <button type="button" class="tab-btn" class:active={activeTab === 'task-defaults'} onclick={() => activeTab = 'task-defaults'}>Task Defaults</button>
            <button type="button" class="tab-btn" class:active={activeTab === 'extensions'} onclick={() => activeTab = 'extensions'}>Extensions</button>
            <button type="button" class="tab-btn" class:active={activeTab === 'about'} onclick={() => activeTab = 'about'}>About</button>
        </div>
        <div class="header-actions">
            {#if !isMobileCtx.isMobile}
                <button type="button" class="icon-btn" onclick={toggleModalFullscreen} title="Toggle fullscreen" aria-label="Toggle fullscreen">
                    {#if modalFullscreen}
                        <svg width="14" height="14" viewBox="0 0 16 16" fill="none"><path d="M10 2V6H14M6 14V10H2" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>
                    {:else}
                        <svg width="14" height="14" viewBox="0 0 16 16" fill="none"><path d="M10 2h4v4M6 14H2v-4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>
                    {/if}
                </button>
            {/if}
            <button class="close-btn" onclick={handleClose} aria-label="Close" title="Close">✕</button>
        </div>
    </div>
{/snippet}

<BaseModal title="Settings" width={modalFullscreen ? '100vw' : '720px'} footer={footer} onClose={handleClose} modalClass={modalFullscreen ? 'config-modal fullscreen-modal' : 'config-modal'} header={customHeader}>
    <!-- Tab Content -->
    <div class="tab-content">

        <!-- Providers Tab -->
        {#if activeTab === 'providers'}
            <div class="section">
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
                            {#if agentsLoading}
                                <div class="agent-menu-item agent-menu-loading">Loading agents...</div>
                            {:else if availableAgents.length === 0}
                                <div class="agent-menu-item agent-menu-empty">No agents installed. Install one from Extensions tab.</div>
                            {:else}
                                {#each availableAgents as ext}
                                    <button type="button" class="agent-menu-item" onclick={() => addAgent(ext)}>
                                        <svg width="14" height="14" viewBox="0 0 16 16" fill="none">
                                            <rect x="3" y="5" width="10" height="8" rx="1.5" stroke="currentColor" stroke-width="1.5"/>
                                            <circle cx="6" cy="9" r="1" fill="currentColor"/>
                                            <circle cx="10" cy="9" r="1" fill="currentColor"/>
                                        </svg>
                                        {ext.manifest.display_name || ext.name}
                                    </button>
                                {/each}
                            {/if}
                        </div>
                    {/if}
                </div>
                <div class="providers-list">
                    {#each providers as p, i (p.uid)}
                        <ProviderCard
                            provider={p}
                            index={i}
                            {defaultProvider}
                            {encryptionEnabled}
                            {currencySymbol}
                            {getModelColor}
                            {parseDefaultModel}
                            isDragging={dragIndex === i}
                            onDragStart={(e: PointerEvent) => handleDragStart(e, i)}
                            onRemove={() => removeProvider(i)}
                            onFetchModels={() => fetchModelsForProvider(i)}
                            onTestConnection={() => testProviderConnection(i)}
                            onToggleDefaultProvider={() => toggleDefaultProvider(p.name)}
                            onAddModel={() => addModelToProvider(i)}
                            onRemoveModel={(mi: number) => removeModelFromProvider(i, mi)}
                            onSetDefaultModel={(name: string, effort?: string) => setProviderDefaultModel(i, name, effort)}
                            onSyncMeta={() => syncProviderMeta(p)}
                            onDirty={markDirty}
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
            </div>
        {/if}

        <!-- Task Defaults Tab -->
        {#if activeTab === 'task-defaults'}
            <TaskDefaults
                bind:defaultOperatingMode
                bind:defaultAutoMerge
                bind:defaultSubtasksAutoMerge
                bind:defaultMaxFailedTurns
                originBranch={appStore.selectedProject?.origin_branch || 'main'}
                onDirty={markDirty}
            />
        {/if}

        <!-- Extensions Tab -->
        {#if activeTab === 'extensions'}
            <ExtensionsTab />
        {/if}

        <!-- About Tab -->
        {#if activeTab === 'about'}
            <div class="section">
                <div class="about-header">
                    <div class="about-icon">{@html pwaIcon}</div>
                    <div>
                        <h3 class="section-label">MIKE: Integrated Kanban Environment <span class="about-version">{config?.version || 'dev'}</span></h3>
                        <p class="about-description">© 2026 Mike Wren</p>
                        <p class="about-description">
                            <a href="https://github.com/SoftwareRenderer" target="_blank" rel="noopener noreferrer">
                                <span>github.com/SoftwareRenderer</span>
                            </a>
                        </p>
                    </div>
                </div>
            </div>

            {#if appStore.pwaInstallPrompt}
            <div class="section">
                <h3 class="section-label">Progress Web App</h3>
                <button type="button" class="btn-pwa-install" onclick={handlePwaInstall} title="Install as a Progressive Web App">
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none">
                        <path d="M8 2v9M5 8l3 3 3-3M3 12v2h10v-2" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                    </svg>
                    Install as PWA
                </button>
            </div>
            {/if}

            <div class="section">
                <h3 class="section-label">System Info</h3>
                <div class="field-label">
                    <span class="field-label-inline">Directory for Worktrees</span>
                    <div class="readonly-value">{config?.workspace_dir || '\u2014'}</div>
                    <span class="field-help">Directory where task worktrees are created</span>
                </div>
                <div class="field-label">
                    <span class="field-label-inline">Database</span>
                    <div class="readonly-value">{config?.database_path || '\u2014'}</div>
                    <span class="field-help">Path to the server's SQLite database</span>
                </div>
            </div>
        {/if}

        {#if error}
            <div id="config-error" class="error" role="alert" aria-live="assertive">{error}</div>
        {/if}
    </div>
</BaseModal>

{#snippet footer()}
    {#if onSignOut}
        <button type="button" class="btn-signout" onclick={handleSignOut} title="Sign out of Kanban">Sign out</button>
    {/if}
    <div class="modal-footer-right">
        {#if activeTab !== 'extensions'}
            <button type="button" class="btn-secondary" onclick={handleClose} title="Cancel and discard changes">Cancel</button>
            <button type="button" class="btn-primary" disabled={loading} onclick={handleSave} title="Save settings">
                {loading ? 'Saving...' : 'Save'}
            </button>
        {/if}
    </div>
{/snippet}

<style>
    @import '$lib/styles/shared.css';

    /* Fixed modal size */
    :global(.config-modal) {
        height: 600px;
        max-height: 600px;
    }

    /* Header wrapper: single flex child in modal-header, single row */
    .header-wrap {
        display: flex;
        align-items: center;
        gap: 16px;
        flex: 1;
        min-width: 0;
    }
    .header-wrap h2 {
        margin: 0;
        font-size: 1.25rem;
        font-weight: 600;
        white-space: nowrap;
    }

    /* Tabs row */
    .header-tabs {
        display: flex;
        gap: 0;
        flex: 1;
        min-width: 0;
    }

    /* Mobile: title + close on top row, tabs below */
    @media (max-width: 768px) {
        .header-wrap {
            flex-wrap: wrap;
        }
        .header-tabs {
            order: 3;
            flex-basis: 100%;
            margin-top: 8px;
        }
        .header-actions {
            order: 2;
            margin-left: auto;
        }
    }
    .tab-btn {
        padding: 8px 16px;
        border: none;
        background: none;
        color: var(--color-text-secondary);
        font-size: 0.875rem;
        font-weight: 500;
        font-family: inherit;
        cursor: pointer;
        border-bottom: 1px solid var(--color-border);
        margin-bottom: -1px;
        transition: color 0.15s, border-color 0.15s;
        white-space: nowrap;
    }
    .tab-btn:hover {
        color: var(--color-text-primary);
    }
    .tab-btn.active {
        color: var(--color-accent);
        border-bottom-color: var(--color-accent);
    }
    .tab-btn:focus-visible {
        outline: 2px solid var(--color-accent);
        outline-offset: -2px;
        border-radius: var(--radius-sm);
    }

    /* Tab content container */
    .tab-content {
        display: flex;
        flex-direction: column;
        gap: 16px;
        padding-bottom: 8px;
    }

    /* Section cards */
    .section {
        border: 1px solid var(--color-border);
        border-radius: var(--radius-md);
        padding: 16px;
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
        color: var(--color-status-purple);
        border-color: var(--color-status-purple-subtle);
    }
    .btn-agent:hover {
        background: var(--color-status-purple-faint);
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
        color: var(--color-status-purple);
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

    /* Toggle row layout */

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
        color: var(--color-status-orange);
        line-height: 1.4;
    }
    .no-llm-warning svg {
        flex-shrink: 0;
        margin-top: 1px;
    }
    .no-llm-warning strong {
        color: var(--color-status-yellow);
    }

    .error {
        color: var(--color-status-red);
        font-size: 0.875rem;
        font-weight: 500;
    }
    .modal-footer-right {
        display: flex;
        gap: 8px;
        margin-left: auto;
    }
    .btn-signout {
        padding: 8px 16px;
        border-radius: var(--radius-md);
        background: var(--color-surface-3);
        color: var(--color-text-primary);
        font-size: 0.875rem;
        font-weight: 500;
        font-family: inherit;
        cursor: pointer;
        border: none;
        transition: background 0.15s;
    }
    .btn-signout:hover {
        background: var(--color-surface-4);
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
        background: var(--color-accent-dark);
        color: white;
    }

    /* About MIKE section */
    .about-header {
        display: flex;
        align-items: flex-start;
        gap: 12px;
    }
    .about-icon {
        width: 48px;
        height: 48px;
        flex-shrink: 0;
    }
    .about-icon :global(svg) {
        width: 100%;
        height: 100%;
        border-radius: var(--radius-md);
    }
    .about-description {
        margin: 4px 0 0;
        font-size: 0.875rem;
        color: var(--color-text-secondary);
        line-height: 1.6;
    }
    .about-version {
        display: inline-block;
        font-size: 0.75rem;
        font-weight: 500;
        font-family: var(--font-mono, 'SF Mono', 'Cascadia Code', 'Fira Code', monospace);
        text-transform: none;
        color: var(--color-text-tertiary);
        background: var(--color-surface-3);
        padding: 2px 8px;
        border-radius: 9999px;
        vertical-align: middle;
        margin-left: 4px;
    }

    /* Hide BaseModal's close button — we render our own in the header snippet */
    :global(.config-modal > .modal-header > .close-btn) {
        display: none;
    }

    /* Header actions (fullscreen toggle + close) */
    .header-actions {
        display: flex;
        align-items: center;
        gap: 4px;
    }
    .icon-btn {
        display: flex;
        align-items: center;
        justify-content: center;
        width: 28px;
        height: 28px;
        padding: 0;
        border: none;
        background: transparent;
        color: var(--color-text-tertiary);
        cursor: pointer;
        border-radius: var(--radius-sm);
        transition: background 0.15s, color 0.15s;
        flex-shrink: 0;
    }
    .icon-btn:hover {
        background: var(--color-surface-3);
        color: var(--color-text-primary);
    }

    /* Fullscreen modal */
    :global(.config-modal.fullscreen-modal) {
        width: 100vw !important;
        max-width: none;
        max-height: 100dvh !important;
        height: 100dvh !important;
        border-radius: 0;
        border: none;
    }
    :global(.config-modal.fullscreen-modal .modal-body) {
        padding: 16px 32px;
    }

    </style>
