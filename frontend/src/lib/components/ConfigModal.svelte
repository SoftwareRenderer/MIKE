<script lang="ts">
    
    import { tick } from 'svelte';
    import type { Config, Provider } from '$lib/types';
    import { isAgentProvider } from '$lib/types';
    import { getModelName, getProviderDisplayName } from '$lib/utils/providers';
    import { t } from '$lib/i18n';
    import { formatMB, usdSymbol } from '$lib/utils/format';
    import { updateConfig, fetchModels as fetchModelsApi, testConnection as testConnectionApi, updateProject } from '$lib/api';
    import { appStore, dialogStore, uiStore } from '$lib/stores';
    import { useIsMobile } from '$lib/composables/use-is-mobile.svelte';
    import BaseModal from './BaseModal.svelte';
    import TaskDefaults from './ConfigModal/TaskDefaults.svelte';
    import ProjectDefaults from './ConfigModal/ProjectDefaults.svelte';
    import type { ProviderDraft } from './ConfigModal/drafts';
    import PreferencesTab from './ConfigModal/PreferencesTab.svelte';
    import ProviderCard from './ConfigModal/ProviderCard.svelte';
    import ExtensionsTab from './ConfigModal/ExtensionsTab.svelte';
    import SessionsSection from './ConfigModal/SessionsSection.svelte';
    import ApiTokensSection from './ConfigModal/ApiTokensSection.svelte';
    import UpdatesSection from './ConfigModal/UpdatesSection.svelte';
    import EmptyState from './EmptyState.svelte';
    import Icon from './Icon.svelte';
    import mikeLogo from '$lib/assets/icons/mike.svg?raw';
    import folderIcon from '$lib/assets/icons/folder.svg?raw';
    import { checkNow, type UpdateOutcome } from '$lib/services/app-update';
    import { updateCheck } from '$lib/services/update-check.svelte';

    let { config, onClose, onSaved, onSignOut }: { config: Config | null; onClose: () => void; onSaved: () => void; onSignOut?: () => void | Promise<void> } = $props();

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
    let runSiblingsParallel = $state(false);
    let reclaimIdleSandbox = $state(false);
    // Keyboard shortcut bindings (undefined = default, "" = disabled).
    // The Save payload always sends BOTH bindings (absent = reset to default).
    let keyboardCreateTask = $state<string | undefined>(undefined);
    let keyboardCreateTaskSubmit = $state<string | undefined>(undefined);
    let keyboardOpenTerminal = $state<string | undefined>(undefined);
    let keyboardStartTask = $state<string | undefined>(undefined);
    let keyboardToggleFullscreen = $state<string | undefined>(undefined);
    let keyboardMultiSelectClick = $state<string | undefined>(undefined);
    let keyboardHideHints = $state(false);
    let hideModelBadge = $state(false);
    // null is "nobody has answered yet", which is not the same state as false: the first-run question
    // is owed until this holds an answer, so an unrelated Save must not write a false of its own.
    let updateCheckEnabled = $state<boolean | null>(null);
    let sortOldestFirst = $state(false);
    let autoMaximizeChat = $state(true);
    let notifyOnReview = $state(false);
    // The open project's answer to the "Assisted-by" commit trailer; absent means on.
    let commitAssistedBy = $state(true);
    let loading = $state(false);
    let error = $state('');

    // The modal is mounted per open, so the tab the opener asked for is read once here. The Project
    // Defaults tab is hidden without a project, so it cannot be the tab asked for.
    let requestedTab = uiStore.settingsTab ?? 'providers';
    if (requestedTab === 'project-defaults' && !appStore.selectedProject) requestedTab = 'providers';
    let activeTab = $state<string>(requestedTab);

    // Track unsaved changes
    let hasChanges = $state(false);
    function markDirty() {
        hasChanges = true;
    }

    // Currency symbol for the pricing inputs, in the ACTIVE locale ("$", "US$", "$US").
    // A $derived, not a once-at-init IIFE: the locale can change while this modal is
    // open, and a snapshot would keep showing the symbol of the language the user left.
    const currencySymbol = $derived(usdSymbol());

    // -- Provider management --
    /** The reference string for a provider: its stable ID (LLM) or name (agent). */
    function providerRef(p: { id?: number; name?: string; kind?: string }): string {
        return p.kind === 'agent' ? (p.name || '') : String(p.id ?? 0);
    }

    /** A row the server derives from an installed agent extension, not one the user typed. */
    function isAgentDraft(p: ProviderDraft): boolean {
        return p.kind === 'agent';
    }

    /** Find a provider draft by its reference string (LLM ID or agent name). */
    function findProviderByRef(ref: string) {
        if (!ref) return null;
        return providers.find(p => providerRef(p) === ref) || null;
    }

    let providers = $state<ProviderDraft[]>([]);
    let _providerUidCounter = 0;

    function buildProviderDraft(p: Provider): ProviderDraft {
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
            stream_idle_seconds: p.stream_idle_seconds || 0,
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
    }

    function buildProviders(cfg: Config | null): ProviderDraft[] {
        return (cfg?.providers || []).map(buildProviderDraft);
    }

    /** What the agent rows are: the enabled agent extensions, as the server derives them.
     *  Nothing the user can edit by hand, which is what makes them safe to reconcile. */
    function agentRowSignature(cfg: Config | null): string {
        return (cfg?.providers || [])
            .filter(isAgentProvider)
            .map(p => `${p.name}\u0000${p.url}\u0000${p.display_name || ''}`)
            .join(',');
    }

    /**
     * Swap the agent rows for the ones the server currently reports, leaving every LLM draft
     * — and the user's ordering of them — untouched. Agents land at the end because that is
     * where the server puts them: persisted providers first, derived ones after.
     */
    function reconcileAgentRows(cfg: Config | null): void {
        const gone = providers.filter(isAgentDraft).map(p => p.name);
        providers = [...providers.filter(p => !isAgentDraft(p)), ...(cfg?.providers || []).filter(isAgentProvider).map(buildProviderDraft)];
        // Only a pair that pointed at one of the agent rows that just disappeared is cleared.
        // An LLM provider can legitimately be referenced by its legacy name — which the server
        // still resolves but this list does not contain — so "not in the list" alone is no
        // evidence to throw away the user's choice.
        if (workerProvider && gone.includes(workerProvider) && !findProviderByRef(workerProvider)) { workerProvider = ''; workerModel = ''; }
        if (utilityProvider && gone.includes(utilityProvider) && !findProviderByRef(utilityProvider)) { utilityProvider = ''; utilityModel = ''; }
    }

    // Sync the form once per dialog open: re-syncing mid-edit discards what the user typed, including a new API key.
    let formSynced = false;
    let agentRows = '';
    $effect(() => {
        if (!config) return;
        if (!formSynced) {
            formSynced = true;
            workerProvider = config.worker_provider || '';
            workerModel = config.worker_model || '';
            utilityProvider = config.utility_provider || '';
            utilityModel = config.utility_model || '';
            defaultAutoCommit = config.default_auto_commit ?? false;
            defaultAutoMerge = config.default_auto_merge ?? false;
            defaultSubtasksAutoMerge = config.default_subtasks_auto_merge ?? true;
            defaultOperatingMode = config.default_operating_mode ?? 'act';
            defaultMaxFailedTurns = config.default_max_failed_turns ?? 3;
            defaultAutoSummarizeThreshold = config.default_auto_summarize_threshold ?? 90;
            runSiblingsParallel = config.run_siblings_parallel ?? false;
            reclaimIdleSandbox = config.reclaim_idle_sandbox ?? false;
            keyboardCreateTask = config.keyboard_shortcuts?.create_task;
            keyboardCreateTaskSubmit = config.keyboard_shortcuts?.create_task_submit;
            keyboardOpenTerminal = config.keyboard_shortcuts?.open_terminal;
            keyboardStartTask = config.keyboard_shortcuts?.start_task;
            keyboardToggleFullscreen = config.keyboard_shortcuts?.toggle_fullscreen;
            keyboardMultiSelectClick = config.keyboard_shortcuts?.multi_select_click;
            keyboardHideHints = config.keyboard_shortcuts?.hide_hints === true;
            hideModelBadge = config.hide_model_badge === true;
            updateCheckEnabled = config.update_check_enabled ?? null;
            sortOldestFirst = config.sort_oldest_first === true;
            autoMaximizeChat = config.auto_maximize_chat_mobile !== false;
            notifyOnReview = uiStore.notifyOnReview;
            commitAssistedBy = appStore.selectedProject?.commit_assisted_by !== false;
            providers = buildProviders(config);
            hasChanges = false;
            agentRows = agentRowSignature(config);
            return;
        }
        const next = agentRowSignature(config);
        if (next === agentRows) return;
        agentRows = next;
        reconcileAgentRows(config);
    });

    async function testProviderConnection(idx: number) {
        const p = providers[idx];
        p.testingConnection = true;
        p.testResult = null;
        try {
            if (p.kind === 'agent') {
                // Agent providers don't have a testable connection (binary is checked at runtime)
                p.testResult = { success: true, message: t('settings.agentConnectionTest') };
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
            stream_idle_seconds: 0,
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

        dragPreview = document.createElement('div');
        dragPreview.className = 'drag-preview';
        dragPreview.style.left = `${e.clientX - 12}px`;
        dragPreview.style.top = `${e.clientY - 12}px`;
        document.body.appendChild(dragPreview);

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
            return t('settings.validateWorker');
        }
        if (!utilityProvider || !utilityModel || !modelValid(utilityProv, utilityModel)) {
            return t('settings.validateUtility');
        }
        return '';
    }

    // Writing `false` because the row was never touched would take the first-run question off the
    // table for good; "declined" is a thing a person said.
    function updateCheckPayload(payload: Config): Config {
        return updateCheckEnabled === null ? payload : { ...payload, update_check_enabled: updateCheckEnabled };
    }

    // Project Defaults writes to the open project's row, not to the config payload. A Save that did not
    // touch the row sends nothing.
    async function saveProjectCommitDefaults() {
        const row = appStore.selectedProject;
        if (!row || commitAssistedBy === (row.commit_assisted_by !== false)) return;
        await updateProject(row.id, { commit_assisted_by: commitAssistedBy });
        // The offline queue answers with a partial row, so the stored row is patched rather than replaced.
        const patched = { ...row, commit_assisted_by: commitAssistedBy };
        appStore.projects = appStore.projects.map(p => (p.id === row.id ? patched : p));
        if (appStore.selectedProject?.id === row.id) appStore.selectedProject = patched;
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
                        stream_idle_seconds: Math.max(0, Math.floor(p.stream_idle_seconds || 0)),
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
            const cfg = await updateConfig(updateCheckPayload({
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
                run_siblings_parallel: runSiblingsParallel,
                reclaim_idle_sandbox: reclaimIdleSandbox,
                hide_model_badge: hideModelBadge,
                sort_oldest_first: sortOldestFirst,
                auto_maximize_chat_mobile: autoMaximizeChat,
                keyboard_shortcuts: { create_task: keyboardCreateTask, create_task_submit: keyboardCreateTaskSubmit, open_terminal: keyboardOpenTerminal, start_task: keyboardStartTask, toggle_fullscreen: keyboardToggleFullscreen, multi_select_click: keyboardMultiSelectClick, hide_hints: keyboardHideHints },
            } as Config));
            appStore.config = cfg;
            await saveProjectCommitDefaults();
            uiStore.notifyOnReview = notifyOnReview;
            uiStore.persistNotifyOnReview();
            // The switch may have just answered the question the dot is waiting on.
            void updateCheck.refresh();
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
                title: t('settings.discardTitle'),
                message: t('settings.discardMessage'),
                confirmText: t('settings.discard'),
                cancelText: t('settings.keepEditing'),
                variant: 'destructive',
            });
            if (!confirmed) return;
        }
        hasChanges = false;
        onClose();
    }

    async function handleSignOut() {
        const confirmed = await dialogStore.showConfirm({
            title: t('settings.signOut'),
            message: t('settings.signOutConfirm'),
            confirmText: t('settings.signOut'),
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

    // Automatic updates are silent; a check this button started has to answer.
    let updateOutcome = $state<UpdateOutcome | null>(null);
    let checkingUpdates = $state(false);
    const updateOutcomeText = $derived(
        updateOutcome === 'current' ? t('settings.upToDate')
            : updateOutcome === 'applying' ? t('settings.updateApplying')
            : t('settings.updateCheckFailed'),
    );

    async function handleCheckUpdates() {
        if (checkingUpdates) return;
        checkingUpdates = true;
        updateOutcome = null;
        try {
            updateOutcome = await checkNow();
        } finally {
            checkingUpdates = false;
        }
    }
</script>

{#snippet customHeader({ title }: { title: string })}
    <div class="header-wrap">
        <h2 id="modal-title">{title}</h2>
        <div class="header-tabs">
            <button type="button" class="tab-btn" class:active={activeTab === 'providers'} onclick={() => activeTab = 'providers'}>{t('settings.tab.providers')}</button>
            {#if appStore.selectedProject}
                <button type="button" class="tab-btn tab-btn-project" class:active={activeTab === 'project-defaults'} onclick={() => activeTab = 'project-defaults'} title={appStore.selectedProject.name}><Icon src={folderIcon} width={12} height={12} class="tab-icon" />{appStore.selectedProject.name}</button>
            {/if}
            <button type="button" class="tab-btn" class:active={activeTab === 'task-defaults'} onclick={() => activeTab = 'task-defaults'}>{t('settings.tab.task')}</button>
            <button type="button" class="tab-btn" class:active={activeTab === 'preferences'} onclick={() => activeTab = 'preferences'}>{t('settings.tab.preferences')}</button>
            <button type="button" class="tab-btn" class:active={activeTab === 'extensions'} onclick={() => activeTab = 'extensions'}>{t('settings.tab.extensions')}</button>
            <button type="button" class="tab-btn" class:active={activeTab === 'about'} onclick={() => activeTab = 'about'}>{t('settings.tab.about')}</button>
        </div>
        <div class="header-actions">
            {#if !isMobileCtx.isMobile}
                <button type="button" class="icon-btn" onclick={toggleModalFullscreen} title={t('settings.toggleFullscreen')} aria-label={t('settings.toggleFullscreen')}>
                    {#if modalFullscreen}
                        <svg width="14" height="14" viewBox="0 0 16 16" fill="none"><path d="M10 2V6H14M6 14V10H2" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>
                    {:else}
                        <svg width="14" height="14" viewBox="0 0 16 16" fill="none"><path d="M10 2h4v4M6 14H2v-4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>
                    {/if}
                </button>
            {/if}
            <button class="close-btn" onclick={handleClose} aria-label={t('settings.close')} title={t('settings.close')}>✕</button>
        </div>
    </div>
{/snippet}

<BaseModal title={t('settings.title')} width={modalFullscreen ? '100vw' : '720px'} footer={footer} onClose={handleClose} modalClass={modalFullscreen ? 'config-modal fullscreen-modal' : 'config-modal'} bodyStyle={activeTab === 'extensions' ? 'padding-top: 0;' : undefined} header={customHeader}>
    <!-- Tab Content -->
    <div class="tab-content">

        <!-- Providers Tab -->
        {#if activeTab === 'providers'}
            <div class="settings-section">
                <div class="section-header">
                    <h3 class="settings-section-title">{t('settings.primaryProviders')}</h3>
                    <span class="field-help">{t('settings.primaryProvidersHelp')}</span>
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
                                aria-label={t('settings.pair.providerAria', { title })}
                            >
                                <option value="">{t('settings.selectProvider')}</option>
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
                                aria-label={t('settings.pair.modelAria', { title })}
                            >
                                <option value="">{t('settings.selectModel')}</option>
                                {#if selProv}
                                    {#if isAgentProvider(selProv)}
                                        {@const agentModels = kind === 'worker' ? workerAgentModels : utilityAgentModels}
                                        {@const agentLoading = kind === 'worker' ? workerAgentModelsLoading : utilityAgentModelsLoading}
                                        {#if agentLoading}
                                            <option value="" disabled>{t('settings.loadingModels')}</option>
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
                    {@render pairRow(t('settings.pair.worker'), t('settings.pair.workerHint'), 'worker')}
                    {@render pairRow(t('settings.pair.utility'), t('settings.pair.utilityHint'), 'utility')}
                </div>
            </div>

            <div class="settings-section">
                <div class="section-header">
                    <h3 class="settings-section-title">{t('settings.providers')}</h3>
                    <div class="settings-section-actions">
                        <button type="button" class="settings-btn-small" onclick={addProvider} title={t('settings.addProviderHint')}>{t('settings.addProvider')}</button>
                    </div>
                </div>
                <div class="providers-list">
                    {#each providers as p, i (p.uid)}
                        <ProviderCard
                            {providers}
                            provider={p}
                            index={i}
                            {currencySymbol}
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
                        <span>{t('settings.noLlmWarning.before')} <strong>{t('settings.noLlmWarning.labels')}</strong> {t('settings.noLlmWarning.between')} <strong>{t('settings.noLlmWarning.resolution')}</strong>.</span>
                    </div>
                {/if}
                {#if providers.length === 0}
                    <EmptyState title={t('settings.noProviders')} hint={t('settings.providersEmptyHint')} />
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
                bind:runSiblingsParallel
                bind:reclaimIdleSandbox
                originBranch={appStore.selectedProject?.origin_branch || 'main'}
                onDirty={markDirty}
            />
        {/if}

        <!-- Project Defaults Tab -->
        {#if activeTab === 'project-defaults' && appStore.selectedProject}
            <ProjectDefaults bind:commitAssistedBy projectName={appStore.selectedProject.name} onDirty={markDirty} />
        {/if}

        <!-- Preferences Tab -->
        {#if activeTab === 'preferences'}
            <PreferencesTab bind:createTaskKey={keyboardCreateTask} bind:createTaskSubmitKey={keyboardCreateTaskSubmit} bind:openTerminalKey={keyboardOpenTerminal} bind:startTaskKey={keyboardStartTask} bind:toggleFullscreenKey={keyboardToggleFullscreen} bind:multiSelectClickKey={keyboardMultiSelectClick} bind:hideHints={keyboardHideHints} bind:hideModelBadge={hideModelBadge} bind:sortOldestFirst={sortOldestFirst} bind:autoMaximizeChat={autoMaximizeChat} bind:notifyOnReview={notifyOnReview} onDirty={markDirty} />
        {/if}

        <!-- Extensions Tab -->
        {#if activeTab === 'extensions'}
            <ExtensionsTab />
        {/if}

        <!-- About Tab -->
        {#if activeTab === 'about'}
            <div class="settings-section">
                <div class="about-header">
                    <div class="about-icon">{@html mikeLogo}</div>
                    <div>
                        <h3 class="settings-section-title">{t('settings.aboutTitle')} <span class="about-version">{config?.version || 'dev'}</span></h3>
                        <p class="about-description">{t('app.copyright', { year: 2026, name: 'Mike Wren' })}</p>
                        <p class="about-description">
                            <a href="https://getmike.dev" target="_blank" rel="noopener noreferrer">
                                <span>getmike.dev</span>
                            </a>
                        </p>
                        <p class="about-update">
                            <button
                                type="button"
                                class="btn-check-updates"
                                onclick={handleCheckUpdates}
                                disabled={checkingUpdates}
                                title={t('settings.checkUpdatesHint')}
                            >{checkingUpdates ? t('settings.checking') : t('settings.checkUpdates')}</button>
                            {#if updateOutcome !== null}
                                <span class="about-update-outcome" role="status">{updateOutcomeText}</span>
                            {/if}
                        </p>
                    </div>
                </div>
            </div>

            <!-- The server's own release: a different question from the one above (that button asks about
                 this browser's assets), and the only place the update answers are given. -->
            <UpdatesSection bind:enabled={updateCheckEnabled} onDirty={markDirty} />

            {#if appStore.pwaInstallPrompt}
            <div class="settings-section">
                <h3 class="settings-section-title">{t('settings.pwa')}</h3>
                <button type="button" class="btn-pwa-install" onclick={handlePwaInstall} title={t('settings.pwaInstallHint')}>
                    <svg width="14" height="14" viewBox="0 0 16 16" fill="none">
                        <path d="M8 2v9M5 8l3 3 3-3M3 12v2h10v-2" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                    </svg>
                    {t('settings.pwaInstall')}
                </button>
            </div>
            {/if}

            <div class="settings-section">
                <h3 class="settings-section-title">{t('settings.systemInfo')}</h3>
                <div class="field-label">
                    <span class="field-label-inline">{t('settings.worktreesDir')}</span>
                    <div class="readonly-value">{config?.workspace_dir || '\u2014'}</div>
                    <span class="field-help">{t('settings.worktreesDirHelp')}</span>
                </div>
                <div class="field-label">
                    <span class="field-label-inline">{t('settings.database')}</span>
                    <div class="readonly-value">{config?.database_path || '\u2014'}</div>
                    <span class="field-help">{t('settings.databaseHelp')}</span>
                </div>
                {#if config?.memory_budget_mb}
                    <!-- 0 means the gate is off (no installed RAM read), and a config written by such a
                         server has nothing to say about memory. -->
                    <div class="field-label">
                        <span class="field-label-inline">{t('settings.maxMemory')}</span>
                        <div class="readonly-value">{formatMB(config.memory_budget_mb)}</div>
                        <span class="field-help">{t('settings.maxMemoryHelp', {
                            pct: config.memory_budget_pct ?? 0,
                            total: formatMB(config.memory_total_mb ?? 0),
                        })}</span>
                    </div>
                {/if}
                {#if config?.tls_ca_available}
                    <!-- Only when the server signed its own certificate. A plain link,
                         not apiFetch: the route is a session-authed GET, so the
                         browser's own download UI handles the file. -->
                    <div class="field-label">
                        <span class="field-label-inline">{t('settings.caCert')}</span>
                        <div>
                            <a class="btn-ca-download" href="/api/tls/ca" download="mike-ca.crt"
                               title={t('settings.caCertHint')}>
                                <svg width="14" height="14" viewBox="0 0 16 16" fill="none">
                                    <path d="M8 2v9M5 8l3 3 3-3M3 12v2h10v-2" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/>
                                </svg>
                                {t('settings.caCertDownload')}
                            </a>
                        </div>
                        <span class="field-help">{t('settings.caCertHelp')}</span>
                    </div>
                {/if}
            </div>

            <!-- Login sessions: mutates immediately (like the PWA button above),
                 so it is deliberately outside the dirty/Save flow. -->
            <SessionsSection onSignOut={onSignOut} />

            <!-- Bearer tokens for headless clients: same rule, same flow. -->
            <ApiTokensSection />
        {/if}

        {#if error}
            <div id="config-error" class="error" role="alert" aria-live="assertive">{error}</div>
        {/if}
    </div>
</BaseModal>

{#snippet footer()}
    {#if onSignOut}
        <!-- `btn-signout` carries no style of its own; the e2e sign-out spec names the button. -->
        <button type="button" class="btn btn-signout" onclick={handleSignOut} title={t('settings.signOutHint')}>{t('settings.signOut')}</button>
    {/if}
    <div class="modal-footer-right">
        {#if activeTab !== 'extensions'}
            <button type="button" class="btn" onclick={handleClose} title={t('settings.cancelHint')}>{t('settings.cancel')}</button>
            <button type="button" class="btn btn-primary" disabled={loading} onclick={handleSave} title={t('settings.saveHint')}>
                {loading ? t('settings.saving') : t('settings.save')}
            </button>
        {/if}
    </div>
{/snippet}

<style>
    @import '$lib/styles/shared.css';
    @import '$lib/styles/settings-section.css';

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
        font-size: var(--text-lg);
        font-weight: 600;
        white-space: nowrap;
    }

    .header-tabs {
        display: flex;
        gap: 0;
        flex: 1;
        min-width: 0;
        flex-wrap: wrap;
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
        font-size: var(--text-base);
        font-weight: 500;
        font-family: inherit;
        cursor: pointer;
        border-bottom: 1px solid var(--color-border);
        margin-bottom: -1px;
        transition: color var(--dur-base), border-color var(--dur-base);
        white-space: nowrap;
    }
    .tab-btn:hover {
        color: var(--color-text-primary);
    }
    .tab-btn-project {
        max-width: 19ch;
        overflow: hidden;
        text-overflow: ellipsis;
    }
    .tab-btn-project :global(.tab-icon) {
        display: inline-flex;
        vertical-align: middle;
        margin-right: 5px;
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

    /* The panel box comes from settings-section.css; only the header row is here. */
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
        font-size: var(--text-base);
        font-weight: 500;
        color: var(--color-text-primary);
    }
    .field-help {
        font-size: var(--text-xs);
        color: var(--color-text-tertiary);
        font-weight: 400;
    }
    .readonly-value {
        font-family: var(--font-mono, 'SF Mono', 'Cascadia Code', 'Fira Code', monospace);
        font-size: var(--text-sm);
        background: var(--color-surface-2);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-md);
        padding: 8px 12px;
        color: var(--color-text-secondary);
        user-select: all;
        overflow-wrap: break-word;
    }
    .field-label-inline {
        font-size: var(--text-base);
        font-weight: 500;
        color: var(--color-text-primary);
    }

    /* Toggle row layout */

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
        font-size: var(--text-base);
        font-weight: 600;
        color: var(--color-text-primary);
    }
    .primary-provider-select {
        font-size: var(--text-sm);
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
        opacity: var(--opacity-disabled);
        cursor: not-allowed;
    }

    /* Providers list container */
    .providers-list {
        display: flex;
        flex-direction: column;
        gap: 8px;
    }
    .no-llm-warning {
        display: flex;
        align-items: flex-start;
        gap: 6px;
        margin-top: 10px;
        padding: 8px 10px;
        background: rgb(from var(--color-warning) r g b / 0.08);
        border: 1px solid rgb(from var(--color-warning) r g b / 0.25);
        border-radius: var(--radius-sm);
        font-size: var(--text-sm);
        color: var(--color-warning);
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
        font-size: var(--text-base);
        font-weight: 500;
    }
    .modal-footer-right {
        display: flex;
        gap: 8px;
        margin-left: auto;
    }
    .btn-pwa-install {
        display: inline-flex;
        align-items: center;
        gap: 6px;
        padding: 8px 14px;
        border-radius: var(--radius-md);
        background: var(--color-surface-3);
        color: var(--color-accent);
        font-size: var(--text-base);
        font-weight: 500;
        cursor: pointer;
        border: 1px solid var(--color-accent);
        transition: background var(--dur-base), color var(--dur-base);
    }
    .btn-pwa-install:hover {
        opacity: 1;
        background: var(--color-accent-dark);
        color: var(--color-btn-text);
    }

    .btn-ca-download {
        display: inline-flex;
        align-items: center;
        gap: 6px;
        width: fit-content;
        padding: 8px 14px;
        border-radius: var(--radius-md);
        background: var(--color-surface-3);
        color: var(--color-accent);
        font-size: var(--text-base);
        font-weight: 500;
        text-decoration: none;
        cursor: pointer;
        border: 1px solid var(--color-accent);
        transition: background var(--dur-base), color var(--dur-base);
    }
    .btn-ca-download:hover {
        background: var(--color-accent-dark);
        color: var(--color-btn-text);
    }

    .btn-check-updates {
        display: inline-flex;
        align-items: center;
        padding: 6px 12px;
        border-radius: var(--radius-md);
        background: var(--color-surface-3);
        color: var(--color-accent);
        font-size: var(--text-sm);
        font-weight: 500;
        font-family: inherit;
        border: 1px solid var(--color-accent);
        transition: background var(--dur-base), color var(--dur-base);
    }
    .btn-check-updates:hover {
        opacity: 1;
        background: var(--color-accent-dark);
        color: var(--color-btn-text);
    }
    .about-update {
        display: flex;
        align-items: center;
        gap: 10px;
        margin: 10px 0 0;
    }
    .about-update-outcome {
        font-size: var(--text-sm);
        color: var(--color-text-secondary);
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
        font-size: var(--text-base);
        color: var(--color-text-secondary);
        line-height: 1.6;
    }
    .about-version {
        display: inline-block;
        font-size: var(--text-xs);
        font-weight: 500;
        font-family: var(--font-mono, 'SF Mono', 'Cascadia Code', 'Fira Code', monospace);
        text-transform: none;
        color: var(--color-text-tertiary);
        background: var(--color-surface-3);
        padding: 2px 8px;
        border-radius: var(--radius-pill);
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
        transition: background var(--dur-base), color var(--dur-base);
        flex-shrink: 0;
    }
    .icon-btn:hover {
        opacity: 1;
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
