<script lang="ts">
    
    import { tick } from 'svelte';
    import type { Config, Provider } from '$lib/types';
    import { isAgentProvider } from '$lib/types';
    import { getModelName, getProviderDisplayName } from '$lib/utils/providers';
    import { updateConfig, getConfig, fetchModels as fetchModelsApi, testConnection as testConnectionApi } from '$lib/api';
    import { appStore, uiStore, dialogStore } from '$lib/stores';
    import { useIsMobile } from '$lib/composables/use-is-mobile.svelte';
    import BaseModal from './BaseModal.svelte';
    import { REASONING_EFFORT_OPTIONS } from '$lib/types';
    import ToggleSwitch from './ToggleSwitch.svelte';
    import TaskDefaults from './ConfigModal/TaskDefaults.svelte';
    import PreferencesTab from './ConfigModal/PreferencesTab.svelte';
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

    let workerProvider = $state('');
    let workerModel = $state('');
    let utilityProvider = $state('');
    let utilityModel = $state('');
    // Agent model lists fetched for the Primary Providers rows (empty for LLM providers).
    let workerAgentModels = $state<Array<{ name: string }>>([]);
    let workerAgentModelsLoading = $state(false);
    let utilityAgentModels = $state<Array<{ name: string }>>([]);
    let utilityAgentModelsLoading = $state(false);
    let defaultAutoCommit = $state(false);
    let defaultAutoMerge = $state(true);
    let defaultSubtasksAutoMerge = $state(true);
    let defaultOperatingMode = $state('act');
    let defaultMaxFailedTurns = $state(3);
    let defaultAutoSummarizeThreshold = $state(90);
    // Keyboard shortcut bindings (undefined = default, "" = disabled).
    // The Save payload always sends BOTH bindings (absent = reset to default).
    let keyboardCreateTask = $state<string | undefined>(undefined);
    let keyboardCreateTaskSubmit = $state<string | undefined>(undefined);
    let keyboardOpenTerminal = $state<string | undefined>(undefined);
    let keyboardStartTask = $state<string | undefined>(undefined);
    let keyboardToggleFullscreen = $state<string | undefined>(undefined);
    let keyboardHideHints = $state(false);
    let hideModelBadge = $state(false);
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
        reasoning_effort?: string; // known level or custom value (passed through to the server)
        context_window?: number | null; // nil if not reported by LLM; editable in UI
        // Per-model overrides; null = inherit provider default
        input_price?: number | null;
        output_price?: number | null;
        cache_read_price?: number | null;
        cache_write_price?: number | null;
        power_consumption_w?: number | null;
    }

    interface ProviderDraft {
        uid: string; // stable key for {#each} — never changes after creation
        id: number; // stable identifier for LLM providers; 0 = new/unsaved
        name: string; // agent provider name (manifest-derived); empty for LLM providers
        display_name?: string; // only agent providers carry a manifest-derived display name
        kind: 'llm' | 'agent';
        url: string;
        api_key: string; // full value when editing, masked ("••••••••••") when loaded from config
        agent_config_dir?: string;
        models: ModelEntry[];
        concurrency: number;
        max_requests_per_second: number; // 0 = unlimited; max new LLM request starts per second
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

    /** The reference string for a provider: its stable ID (LLM) or name (agent). */
    function providerRef(p: { id?: number; name?: string; kind?: string }): string {
        return p.kind === 'agent' ? (p.name || '') : String(p.id ?? 0);
    }

    /** Find a provider draft by its reference string (LLM ID or agent name). */
    function findProviderByRef(ref: string) {
        if (!ref) return null;
        return providers.find(p => providerRef(p) === ref) || null;
    }

    let providers = $state<ProviderDraft[]>([]);
    let encryptionEnabled = $state(false);
    let _providerUidCounter = 0;

    function buildProviders(cfg: Config | null): ProviderDraft[] {
        return (cfg?.providers || []).map(p => {
            // Detect agent providers (kind field or agent:// URL prefix)
            const isAgent = isAgentProvider(p);
            const draft: ProviderDraft = {
                uid: isAgent ? (p.name || p.url) : String(p.id ?? `new-${Date.now()}-${Math.random()}`),
                id: p.id || 0,
                name: p.name || '',
                ...p,
                kind: isAgent ? 'agent' : (p.kind || 'llm'),
                api_key: p.api_key || '',
                concurrency: p.concurrency || 1,
                max_requests_per_second: p.max_requests_per_second || 0,
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
                agent_config_dir: isAgent ? (p.agent_config_dir || '') : undefined,
                models: isAgent ? [] : (p.models || []).map(m => {
                    // Handle both old format (string) and new format ({name, reasoning_effort, ...})
                    if (typeof m === 'string') {
                        return { name: m, readonly: false, reasoning_effort: 'default' as const, context_window: null, input_price: null, output_price: null, cache_read_price: null, cache_write_price: null, power_consumption_w: null };
                    }
                    return {
                        name: m.name,
                        readonly: false,
                        reasoning_effort: m.reasoning_effort || 'default',
                        context_window: m.context_window ?? null,
                        input_price: m.input_price ?? null,
                        output_price: m.output_price ?? null,
                        cache_read_price: m.cache_read_price ?? null,
                        cache_write_price: m.cache_write_price ?? null,
                        power_consumption_w: m.power_consumption_w ?? null,
                    };
                }).sort((a, b) => a.name.localeCompare(b.name, undefined, { sensitivity: 'base' })),
            };
            return draft;
        });
    }

    // Sync form fields and providers from config on mount and when config changes
    $effect(() => {
        workerProvider = config?.worker_provider || '';
        workerModel = config?.worker_model || '';
        utilityProvider = config?.utility_provider || '';
        utilityModel = config?.utility_model || '';
        defaultAutoCommit = config?.default_auto_commit ?? false;
        defaultAutoMerge = config?.default_auto_merge ?? false;
        defaultSubtasksAutoMerge = config?.default_subtasks_auto_merge ?? true;
        defaultOperatingMode = config?.default_operating_mode ?? 'act';
        defaultMaxFailedTurns = config?.default_max_failed_turns ?? 3;
        defaultAutoSummarizeThreshold = config?.default_auto_summarize_threshold ?? 90;
        keyboardCreateTask = config?.keyboard_shortcuts?.create_task;
        keyboardCreateTaskSubmit = config?.keyboard_shortcuts?.create_task_submit;
        keyboardOpenTerminal = config?.keyboard_shortcuts?.open_terminal;
        keyboardStartTask = config?.keyboard_shortcuts?.start_task;
        keyboardToggleFullscreen = config?.keyboard_shortcuts?.toggle_fullscreen;
        keyboardHideHints = config?.keyboard_shortcuts?.hide_hints === true;
        hideModelBadge = config?.hide_model_badge === true;
        encryptionEnabled = config?.encryption_enabled ?? false;
        providers = buildProviders(config);
        hasChanges = false;
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

                if (p.models.length === 0) {
                    await fetchModelsForProvider(idx);
                }
                const defaultModelName = p.models.find(m => m.name)?.name || '';
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

    // Matches the `transition:slide={{ duration: 200 }}` in ProviderCard.svelte —
    // wait for the slide-in to settle so the scroll target has its final height.
    const PROVIDER_CARD_SLIDE_SETTLE_MS = 250;

    /**
     * After a new provider card is appended, scroll the card's input area into the modal's visible area
     */
    async function revealNewProvider(index: number) {
        await tick();
        await new Promise(r => setTimeout(r, PROVIDER_CARD_SLIDE_SETTLE_MS));
        // Target the URL input
        const urlInput = document.getElementById(`provider-url-${index}`);
        if (!urlInput) return;
        urlInput.scrollIntoView({ block: 'nearest', behavior: 'smooth' });
        urlInput.focus({ preventScroll: true });
    }

    function addProvider() {
        providers.push({
            uid: `new-${++_providerUidCounter}-${Date.now()}`,
            id: 0,
            name: '',
            kind: 'llm',
            url: '',
            api_key: '',
            models: [],
            concurrency: 1,
            max_requests_per_second: 0,
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
        void revealNewProvider(providers.length - 1);
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
                // Preserve user-entered pricing/power across re-fetch; refresh context.
                const existing = new Map(p.models.map(m => [m.name, m]));
                p.models = result.models.map(m => {
                    const prior = existing.get(m.name);
                    return {
                        name: m.name,
                        readonly: true,
                        reasoning_effort: prior?.reasoning_effort || 'default',
                        context_window: m.context_window ?? prior?.context_window ?? null,
                        input_price: prior?.input_price ?? null,
                        output_price: prior?.output_price ?? null,
                        cache_read_price: prior?.cache_read_price ?? null,
                        cache_write_price: prior?.cache_write_price ?? null,
                        power_consumption_w: prior?.power_consumption_w ?? null,
                    };
                });
                p.models.sort((a, b) => a.name.localeCompare(b.name, undefined, { sensitivity: 'base' }));
            }
        } catch (e) {
            p.fetchError = String(e);
        } finally {
            p.fetchingModels = false;
        }
    }

    // Fetch the agent's available models for a Primary Providers row when an agent
    // is selected, mirroring the ModelSelector behavior used when creating a task.
    async function fetchPrimaryAgentModels(kind: 'worker' | 'utility', url: string) {
        if (!url) {
            if (kind === 'worker') workerAgentModels = [];
            else utilityAgentModels = [];
            return;
        }
        if (kind === 'worker') workerAgentModelsLoading = true;
        else utilityAgentModelsLoading = true;
        try {
            const result = await fetchModelsApi(url);
            if (!result.error) {
                const models = (result.models || []).map(m => ({ name: m.name }));
                models.sort((a, b) => a.name.localeCompare(b.name, undefined, { sensitivity: 'base' }));
                if (kind === 'worker') workerAgentModels = models;
                else utilityAgentModels = models;
                // Auto-select the first available model when nothing is selected yet,
                // mirroring ModelSelector behavior when creating a task.
                if (kind === 'worker' && models.length > 0 && (!workerModel || workerModel === workerProvider)) {
                    workerModel = models[0].name;
                }
                if (kind === 'utility' && models.length > 0 && (!utilityModel || utilityModel === utilityProvider)) {
                    utilityModel = models[0].name;
                }
            } else if (kind === 'worker') {
                workerAgentModels = [];
            } else {
                utilityAgentModels = [];
            }
        } catch (e) {
            console.error('Failed to fetch agent models:', e);
            if (kind === 'worker') workerAgentModels = [];
            else utilityAgentModels = [];
        } finally {
            if (kind === 'worker') workerAgentModelsLoading = false;
            else utilityAgentModelsLoading = false;
        }
    }

    // Fetch agent models for the Primary Providers rows when the selected provider is an agent.
    $effect(() => {
        const workerProv = findProviderByRef(workerProvider);
        if (workerProv && isAgentProvider(workerProv)) fetchPrimaryAgentModels('worker', workerProv.url);
        else workerAgentModels = [];
    });
    $effect(() => {
        const utilityProv = findProviderByRef(utilityProvider);
        if (utilityProv && isAgentProvider(utilityProv)) fetchPrimaryAgentModels('utility', utilityProv.url);
        else utilityAgentModels = [];
    });

    function addModelToProvider(idx: number) {
        providers[idx].models.push({ name: '', readonly: false, reasoning_effort: 'default', context_window: null, input_price: null, output_price: null, cache_read_price: null, cache_write_price: null, power_consumption_w: null });
        providers[idx].models.sort((a, b) => a.name.localeCompare(b.name, undefined, { sensitivity: 'base' }));
    }

    function removeModelFromProvider(idx: number, modelIdx: number) {
        const p = providers[idx];
        p.models.splice(modelIdx, 1);
    }

    /** Validate that both Worker and Utility pairs are selected and reference a valid provider/model. */
    function validatePrimaryProviders(): string {
        const workerProv = findProviderByRef(workerProvider);
        const utilityProv = findProviderByRef(utilityProvider);
        const modelValid = (prov: ProviderDraft | null, model: string) => {
            if (!prov) return false;
            if (prov.kind === 'agent') return !!model;
            return !!model && prov.models.some(m => m.name.trim() === model);
        };
        if (!workerProvider || !workerModel || !modelValid(workerProv, workerModel)) {
            return 'Please select a Worker provider and model.';
        }
        if (!utilityProvider || !utilityModel || !modelValid(utilityProv, utilityModel)) {
            return 'Please select a Utility provider and model.';
        }
        return '';
    }

    async function handleSave() {
        const validationError = validatePrimaryProviders();
        if (validationError) {
            error = validationError;
            return;
        }
        loading = true;
        error = '';
        try {
            // Agent providers are registry-derived (installed+enabled agent
            // extensions) and are never persisted — exclude them from the payload.
            const filteredProviders = providers.filter(p => p.kind !== 'agent' && p.url.trim());
            const savedProviders: Provider[] = filteredProviders.map((p) => {
                    const trimmedKey = p.api_key?.trim() || '';
                    // Masked keys are all bullets — don't send them. Only send real values.
                    const isMasked = trimmedKey && trimmedKey.replace(/•/g, '').length === 0;
                    const base: Provider = {
                        id: p.id || undefined,
                        name: p.kind === 'agent' ? p.name.trim() : '',
                        kind: p.kind,
                        url: p.url.trim(),
                        models: p.models.filter(m => m.name.trim()).map(m => ({
                            name: m.name.trim(),
                            reasoning_effort: (m.reasoning_effort || '').trim() || 'default',
                            // undefined = omitted (inherit provider default)
                            context_window: m.context_window ?? undefined,
                            input_price: m.input_price ?? undefined,
                            output_price: m.output_price ?? undefined,
                            cache_read_price: m.cache_read_price ?? undefined,
                            cache_write_price: m.cache_write_price ?? undefined,
                            power_consumption_w: m.power_consumption_w ?? undefined,
                        })),
                        concurrency: Math.max(1, p.concurrency || 1),
                        max_requests_per_second: Math.max(0, Math.floor(p.max_requests_per_second || 0)),
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
                    // Only agent providers carry a (manifest-derived) display name — LLM display
                    // names are derived dynamically from the URL and are never persisted.
                    if (p.kind === 'agent') {
                        base.display_name = p.display_name?.trim() || '';
                    }
                    return base;
                });
            const cfg = await updateConfig({
                workspace_dir: config?.workspace_dir || '',
                providers: savedProviders,
                worker_provider: workerProvider,
                worker_model: workerModel,
                utility_provider: utilityProvider,
                utility_model: utilityModel,
                default_auto_commit: defaultAutoCommit,
                default_auto_merge: defaultAutoMerge,
                default_subtasks_auto_merge: defaultSubtasksAutoMerge,
                default_operating_mode: defaultOperatingMode,
                default_max_failed_turns: defaultMaxFailedTurns,
                default_auto_summarize_threshold: defaultAutoSummarizeThreshold,
                hide_model_badge: hideModelBadge,
                keyboard_shortcuts: { create_task: keyboardCreateTask, create_task_submit: keyboardCreateTaskSubmit, open_terminal: keyboardOpenTerminal, start_task: keyboardStartTask, toggle_fullscreen: keyboardToggleFullscreen, hide_hints: keyboardHideHints },
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
            <button type="button" class="tab-btn" class:active={activeTab === 'preferences'} onclick={() => activeTab = 'preferences'}>Preferences</button>
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

<BaseModal title="Settings" width={modalFullscreen ? '100vw' : '720px'} footer={footer} onClose={handleClose} modalClass={modalFullscreen ? 'config-modal fullscreen-modal' : 'config-modal'} bodyStyle={activeTab === 'extensions' ? 'padding-top: 0;' : undefined} header={customHeader}>
    <!-- Tab Content -->
    <div class="tab-content">

        <!-- Providers Tab -->
        {#if activeTab === 'providers'}
            <div class="section">
                <div class="section-header">
                    <h3 class="section-label">Primary Providers</h3>
                    <span class="field-help">Select default provider/model for each task type.</span>
                </div>
                <div class="primary-providers-list">
                    {#snippet pairRow(title: string, hint: string, kind: 'worker' | 'utility')}
                        {@const selProvider = kind === 'worker' ? workerProvider : utilityProvider}
                        {@const selModel = kind === 'worker' ? workerModel : utilityModel}
                        {@const selProv = findProviderByRef(selProvider)}
                        <div class="primary-provider-row">
                            <div class="primary-provider-info">
                                <span class="primary-provider-title">{title}</span>
                                <span class="field-help">{hint}</span>
                            </div>
                            <select
                                class="primary-provider-select"
                                value={selProvider}
                                onchange={(e) => {
                                    const val = (e.target as HTMLSelectElement).value;
                                    if (kind === 'worker') { workerProvider = val; workerModel = ''; }
                                    else { utilityProvider = val; utilityModel = ''; }
                                    markDirty();
                                }}
                                aria-label="{title} provider"
                            >
                                <option value="">Select provider</option>
                                {#each providers as p}
                                    <option value={providerRef(p)}>{getProviderDisplayName(p, providers)}</option>
                                {/each}
                            </select>
                            <select
                                class="primary-provider-select"
                                value={selModel}
                                onchange={(e) => {
                                    const val = (e.target as HTMLSelectElement).value;
                                    if (kind === 'worker') { workerModel = val; }
                                    else { utilityModel = val; }
                                    markDirty();
                                }}
                                disabled={!selProv || (selProv && !isAgentProvider(selProv) && !selProv.models.length)}
                                aria-label="{title} model"
                            >
                                <option value="">Select model</option>
                                {#if selProv}
                                    {#if isAgentProvider(selProv)}
                                        {@const agentModels = kind === 'worker' ? workerAgentModels : utilityAgentModels}
                                        {@const agentLoading = kind === 'worker' ? workerAgentModelsLoading : utilityAgentModelsLoading}
                                        {#if agentLoading}
                                            <option value="" disabled>Loading models...</option>
                                        {:else if agentModels.length > 0}
                                            {#each agentModels as m}
                                                <option value={m.name} selected={selModel === m.name}>{m.name}</option>
                                            {/each}
                                        {:else}
                                            <option value={selProvider} selected={selModel === selProvider}>{selProvider}</option>
                                        {/if}
                                    {:else}
                                        {#each selProv.models.filter(m => m.name.trim()) as m}
                                            <option value={getModelName(m)} selected={selModel === getModelName(m)}>{getModelName(m)}</option>
                                        {/each}
                                    {/if}
                                {/if}
                            </select>
                        </div>
                    {/snippet}
                    {@render pairRow('Worker', 'Model used for chat responses', 'worker')}
                    {@render pairRow('Utility', 'Model for summaries, commit messages, labels', 'utility')}
                </div>
            </div>

            <div class="section">
                <div class="section-header">
                    <h3 class="section-label">Providers</h3>
                    <div class="section-actions">
                        <button type="button" class="btn-small" onclick={addProvider} title="Add a new LLM provider">+ Add Provider</button>
                    </div>
                </div>
                <div class="providers-list">
                    {#each providers as p, i (p.uid)}
                        <ProviderCard
                            {providers}
                            provider={p}
                            index={i}
                            {encryptionEnabled}
                            {currencySymbol}
                            {getModelColor}
                            isDragging={dragIndex === i}
                            onDragStart={(e: PointerEvent) => handleDragStart(e, i)}
                            onRemove={() => removeProvider(i)}
                            onFetchModels={() => fetchModelsForProvider(i)}
                            onTestConnection={() => testProviderConnection(i)}
                            onAddModel={() => addModelToProvider(i)}
                            onRemoveModel={(mi: number) => removeModelFromProvider(i, mi)}
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
                bind:defaultAutoSummarizeThreshold
                originBranch={appStore.selectedProject?.origin_branch || 'main'}
                onDirty={markDirty}
            />
        {/if}

        <!-- Preferences Tab -->
        {#if activeTab === 'preferences'}
            <PreferencesTab bind:createTaskKey={keyboardCreateTask} bind:createTaskSubmitKey={keyboardCreateTaskSubmit} bind:openTerminalKey={keyboardOpenTerminal} bind:startTaskKey={keyboardStartTask} bind:toggleFullscreenKey={keyboardToggleFullscreen} bind:hideHints={keyboardHideHints} bind:hideModelBadge={hideModelBadge} onDirty={markDirty} />
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

    @media (max-width: 768px) {
        .header-wrap {
            flex-wrap: wrap;
        }
        .header-tabs {
            order: 3;
            flex-basis: 100%;
            margin-top: 8px;
            overflow-x: auto;
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

    /* When modal-body padding-top is 0 (extensions tab), restore spacing for other tabs */
    :global(.modal-body[style*="padding-top: 0"]) .tab-content > :not(.extensions-tab) {
        padding-top: 8px;
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

    /* Primary Providers list container */
    .primary-providers-list {
        display: flex;
        flex-direction: column;
        gap: 10px;
    }
    .primary-provider-row {
        display: grid;
        grid-template-columns: 1fr 1fr 1fr;
        align-items: center;
        gap: 10px;
    }
    .primary-provider-info {
        display: flex;
        flex-direction: column;
        gap: 2px;
        min-width: 0;
    }
    .primary-provider-title {
        font-size: 0.875rem;
        font-weight: 600;
        color: var(--color-text-primary);
    }
    .primary-provider-select {
        font-size: 0.8125rem;
        padding: 6px 8px;
        background: var(--color-surface-1);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
        color: var(--color-text-primary);
        min-width: 0;
    }
    .primary-provider-select:focus {
        outline: none;
        border-color: var(--color-accent);
    }
    .primary-provider-select:disabled {
        opacity: 0.5;
        cursor: not-allowed;
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
