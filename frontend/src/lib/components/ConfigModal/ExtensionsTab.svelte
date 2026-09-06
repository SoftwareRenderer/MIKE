<script lang="ts">
    import type { Extension, RegistryEntry, ExtensionUpdate, SandboxMount, SandboxEnv } from '$lib/types';
    import { listExtensions, listLocalExtensions, getRegistryIndex, installExtension, installLocalExtension, deleteExtension, setExtensionEnabled, setDependencyCache, getHomeDirectory, checkUpdates, updateExtension, updateExtensionImage } from '$lib/api';
    import ToggleSwitch from '../ToggleSwitch.svelte';
    import BaseModal from '../BaseModal.svelte';
    import { dialogStore } from '$lib/stores';
    import { isDangerousPermission, requiresInstallReview, sortPermissions, dependencyCacheDeclaration, needsDependencyGrant, requiresSetupAfterInstall } from '$lib/utils/extension-permissions';
    import { effectiveConfigFields, initialEditConfig, HOST_OWNED_LSP_CONFIG_KEY, type ConfigField } from '$lib/utils/extension-config';
    import { memoryLimitToInput, parseMemoryLimitMB } from '$lib/utils/memory-stats';
    import { timeAgo } from '$lib/utils/format-time';
    import { orderBrowseEntries } from '$lib/composables/use-browse-order';
    import { identityKey } from '$lib/utils/extension-identity';
    import { t } from '$lib/i18n';

    let { onExtensionsChanged }: { onExtensionsChanged?: () => void } = $props();

    /** Validate a URL has an allowed scheme (http/https). Returns '#' for unsafe URLs. */
    function safeUrl(url: string): string {
        if (!url) return '#';
        try {
            const u = new URL(url);
            if (!['http:', 'https:'].includes(u.protocol)) return '#';
            return url;
        } catch {
            return '#';
        }
    }

    /**
     * Identity of an extension is `<slugified-author>/<name>` — see
     * $lib/utils/extension-identity, which the e2e Browse-composition helper shares.
     */

    // State
    let extensions = $state<Extension[]>([]);
    let registryEntries = $state<RegistryEntry[]>([]);
    let localExtensions = $state<RegistryEntry[]>([]);
    let updates = $state<ExtensionUpdate[]>([]);
    let activeView = $state<'installed' | 'browse'>('installed');
    let loading = $state(false);
    let loadingRegistry = $state(false);
    let loadingUpdates = $state(false);
    let hasCheckedUpdates = $state(false);
    let updatingAll = $state(false);
    let error = $state('');
    let searchQuery = $state('');

    // Extension form state
    let editExtensionId = $state<string | null>(null);
    let editingExtension = $state<Extension | null>(null);
    let editConfig = $state<Record<string, any>>({});
    let editLspSettings = $state<{ editor_enabled: boolean; tool_calls_enabled: boolean }>({ editor_enabled: true, tool_calls_enabled: true });
    let saving = $state(false);
    let installing = $state<Set<string>>(new Set());
    let updating = $state<Set<string>>(new Set());
    // Per-extension build progress, shown compactly in the matching card.
    let buildProgress = $state<Record<string, { progress: number; message: string; error?: string }>>({});

    // Capabilities modal
    let showCapabilitiesModal = $state(false);
    let selectedExtension: Extension | null = $state(null);
    // Extension id whose local path is revealed (clicked its "Local" badge).
    let revealedLocalPath = $state<string | null>(null);

    // Install confirmation: set when the user clicks Install on an extension
    // that declares the network capability or sandbox-escaping permissions;
    // the dialog reviews the network capability (allowed domains) and the full
    // declared permission list before the install starts.
    let pendingInstall = $state<{ entry: RegistryEntry; isLocal: boolean } | null>(null);

    // Human-readable labels for UI injection points
    const INJECTION_POINT_LABELS = $derived<Record<string, string>>({
        'taskCard': t('settings.extensions.point.taskCard'),
        'fileEditor': t('settings.extensions.point.fileEditor'),
        'projectBoard': t('settings.extensions.point.projectBoard'),
        'taskChat': t('settings.extensions.point.taskChat'),
        'projectSidebar': t('settings.extensions.point.projectSidebar'),
        'fileBrowser': t('settings.extensions.point.fileBrowser'),
        'createTask': t('settings.extensions.point.createTask'),
    });

    // Capability descriptions
    const CAPABILITY_DESCRIPTIONS = $derived<Record<string, { label: string; description: string }>>({
        network: {
            label: t('settings.extensions.cap.network.label'),
            description: t('settings.extensions.cap.network.description'),
        },
        lsp: {
            label: t('settings.extensions.cap.lsp.label'),
            description: t('settings.extensions.cap.lsp.description'),
        },
        ui: {
            label: t('settings.extensions.cap.ui.label'),
            description: t('settings.extensions.cap.ui.description'),
        },
        css: {
            label: t('settings.extensions.cap.css.label'),
            description: t('settings.extensions.cap.css.description'),
        },
        agent: {
            label: t('settings.extensions.cap.agent.label'),
            description: t('settings.extensions.cap.agent.description'),
        },
        llm: {
            label: t('settings.extensions.cap.llm.label'),
            description: t('settings.extensions.cap.llm.description'),
        },
        i18n: {
            label: t('settings.extensions.cap.i18n.label'),
            description: t('settings.extensions.cap.i18n.description'),
        },
    });
    // Human-readable explanations for sandboxed UI extension permissions.
    // Non-technical, user-facing wording. Only permissions an extension
    // actually DECLARES are listed — always-granted ones (config.get,
    // log.write, theme.get, ui.status) and implied ones (e.g.
    // editor.removeFooterButton, editor.contentChange, preview.serve,
    // preview.release, preview.navState) have no entry. Unknown/legacy names
    // fall back to the raw name in the UI.
    const PERMISSION_DESCRIPTIONS = $derived<Record<string, { label: string; description: string }>>({
        'createTask.description.insertText': {
            label: t('settings.extensions.perm.createTaskDescriptionInsertText.label'),
            description: t('settings.extensions.perm.createTaskDescriptionInsertText.description'),
        },
        'createTask.events.paste': {
            label: t('settings.extensions.perm.createTaskEventsPaste.label'),
            description: t('settings.extensions.perm.createTaskEventsPaste.description'),
        },
        'network.proxy': {
            label: t('settings.extensions.perm.networkProxy.label'),
            description: t('settings.extensions.perm.networkProxy.description'),
        },
        'ui.setSize': {
            label: t('settings.extensions.perm.uiSetSize.label'),
            description: t('settings.extensions.perm.uiSetSize.description'),
        },
        'ui.modal': {
            label: t('settings.extensions.perm.uiModal.label'),
            description: t('settings.extensions.perm.uiModal.description'),
        },
        'storage.get': {
            label: t('settings.extensions.perm.storageGet.label'),
            description: t('settings.extensions.perm.storageGet.description'),
        },
        'storage.set': {
            label: t('settings.extensions.perm.storageSet.label'),
            description: t('settings.extensions.perm.storageSet.description'),
        },
        'storage.delete': {
            label: t('settings.extensions.perm.storageDelete.label'),
            description: t('settings.extensions.perm.storageDelete.description'),
        },
        'storage.list': {
            label: t('settings.extensions.perm.storageList.label'),
            description: t('settings.extensions.perm.storageList.description'),
        },
        'editor.addFooterButton': {
            label: t('settings.extensions.perm.editorAddFooterButton.label'),
            description: t('settings.extensions.perm.editorAddFooterButton.description'),
        },
        'editor.getContent': {
            label: t('settings.extensions.perm.editorGetContent.label'),
            description: t('settings.extensions.perm.editorGetContent.description'),
        },
        'editor.setContent': {
            label: t('settings.extensions.perm.editorSetContent.label'),
            description: t('settings.extensions.perm.editorSetContent.description'),
        },
        'editor.setOverlay': {
            label: t('settings.extensions.perm.editorSetOverlay.label'),
            description: t('settings.extensions.perm.editorSetOverlay.description'),
        },
        'editor.jumpToLine': {
            label: t('settings.extensions.perm.editorJumpToLine.label'),
            description: t('settings.extensions.perm.editorJumpToLine.description'),
        },
        'editor.save': {
            label: t('settings.extensions.perm.editorSave.label'),
            description: t('settings.extensions.perm.editorSave.description'),
        },
        'preview.serveScripts': {
            label: t('settings.extensions.perm.previewServeScripts.label'),
            description: t('settings.extensions.perm.previewServeScripts.description'),
        },
        'preview.serveNetwork': {
            label: t('settings.extensions.perm.previewServeNetwork.label'),
            description: t('settings.extensions.perm.previewServeNetwork.description'),
        },
    });

    // Get list of capability keys for an installed extension
    function getCapabilityKeys(ext: Extension): string[] {
        const caps = ext.manifest.capabilities;
        if (!caps) return [];
        const keys: string[] = [];
        if (caps.lsp) keys.push('lsp');
        if (caps.ui) keys.push('ui');
        if (caps.css) keys.push('css');
        if (caps.agent) keys.push('agent');
        if (caps.network) keys.push('network');
        if (caps.llm) keys.push('llm');
        if (caps.i18n) keys.push('i18n');
        return keys;
    }

    /**
     * The locale an `i18n` pack declares. It is manifest data, so it is read through an
     * untyped accessor rather than the capability struct: the pack names its own language.
     */
    function packLocale(capabilities: unknown): string {
        const code = (capabilities as { i18n?: { locale?: unknown } } | undefined)?.i18n?.locale;
        return typeof code === 'string' ? code : '';
    }

    // Get capability keys for a registry entry.
    // Prefers the entry's declared capabilities (which may include e.g. network
    // alongside ui), falling back to the capability implied by its type.
    function getRegistryCapabilityKeys(entry: RegistryEntry): string[] {
        if (entry.capabilities && Object.keys(entry.capabilities).length > 0) {
            return Object.keys(entry.capabilities).filter(k => !!CAPABILITY_DESCRIPTIONS[k]);
        }
        const capMap: Record<string, string> = { lsp: 'lsp', ui: 'ui', css: 'css', agent: 'agent', llm: 'llm', i18n: 'i18n' };
        const k = capMap[entry.type];
        return k ? [k] : [];
    }

    // Build a capabilities object for a registry entry. Uses the entry's
    // declared capability config when present (so the modal can show the same
    // details as the Installed tab), otherwise empty objects per capability.
    function getRegistryCapabilities(entry: RegistryEntry): Record<string, any> {
        if (entry.capabilities && Object.keys(entry.capabilities).length > 0) {
            return entry.capabilities;
        }
        const caps: Record<string, any> = {};
        for (const k of getRegistryCapabilityKeys(entry)) caps[k] = {};
        return caps;
    }

    // Badge text for an extension type. An unrecognized type still renders UI
    // because that is what a manifest the host does not know most likely is —
    // but a translation pack must not wear that label, or the row the user came
    // to Settings to toggle is indistinguishable from an iframe extension.
    function typeBadge(type: string): string {
        switch (type) {
            case 'lsp': return 'LSP';
            case 'css': return 'CSS';
            case 'agent': return 'AGENT';
            case 'llm': return 'LLM';
            case 'i18n': return 'I18N';
            default: return 'UI';
        }
    }

    // Load extensions on mount
    async function loadExtensions() {
        loading = true;
        error = '';
        try {
            extensions = await listExtensions();
            onExtensionsChanged?.();
        } catch (e: any) {
            error = e.message || t('settings.extensions.loadFailed');
        } finally {
            loading = false;
        }
    }

    // Load extensions present in the local extensions directory (filesystem).
    // Best-effort: a failure here should not block browsing the registry.
    async function loadLocalExtensions() {
        try {
            localExtensions = await listLocalExtensions();
        } catch {
            localExtensions = [];
        }
    }

    // Load registry
    async function loadRegistry() {
        loadingRegistry = true;
        error = '';
        try {
            registryEntries = await getRegistryIndex();
        } catch (e: any) {
            error = e.message || t('settings.extensions.registryLoadFailed');
        } finally {
            loadingRegistry = false;
        }
    }

    // Remove a per-extension progress entry (reactive reassignment).
    function clearBuildProgress(name: string) {
        const next = { ...buildProgress };
        delete next[name];
        buildProgress = next;
    }

    // Record the latest progress event for an extension.
    function setBuildProgress(name: string, progress: number, message: string, error?: string) {
        buildProgress = { ...buildProgress, [name]: { progress, message, error } };
    }

    // Kick off an install from Browse. Extensions that declare the network
    // capability or sandbox-escaping permissions first go through the install
    // confirm dialog, so the user reviews what they are granting before the
    // install starts.
    function handleInstall(entry: RegistryEntry, isLocal: boolean) {
        if (requiresInstallReview(entry)) {
            pendingInstall = { entry, isLocal };
            return;
        }
        void runInstall(entry, isLocal);
    }

    // Confirm the pending install from the dialog and start it.
    function confirmPendingInstall() {
        const pending = pendingInstall;
        pendingInstall = null;
        if (pending) void runInstall(pending.entry, pending.isLocal);
    }

    // Install an extension, streaming progress into the card as it happens.
    // Local (unpublished) extensions install from the filesystem, bypassing the registry.
    async function runInstall(entry: RegistryEntry, isLocal: boolean) {
        const name = entry.name;
        if (installing.has(name)) return;
        error = '';
        installing = new Set(installing).add(name);
        try {
            const stream = isLocal ? installLocalExtension(entry.author, name) : installExtension(name);
            for await (const event of stream) {
                setBuildProgress(event.extension, event.progress, event.message, event.error);
                if (event.done) {
                    if (event.error) {
                        error = event.error;
                        setTimeout(() => clearBuildProgress(event.extension), 6000);
                    } else {
                        setTimeout(() => clearBuildProgress(event.extension), 2000);
                    }
                }
            }
            await loadExtensions();
        } catch (e: any) {
            error = e.message || t('settings.extensions.installFailed');
        } finally {
            const next = new Set(installing);
            next.delete(name);
            installing = next;
        }
    }

    // Delete extension
    async function handleDelete(id: string, name: string) {
        const confirmed = await dialogStore.showConfirm({
            title: t('settings.extensions.deleteTitle', { name }),
            message: t('settings.extensions.deleteMessage'),
            confirmText: t('settings.extensions.delete'),
            cancelText: t('settings.cancel'),
        });
        if (!confirmed) return;

        error = '';
        try {
            const result = await deleteExtension(id);
            await loadExtensions();
            if (result.leftover) {
                error = t('settings.extensions.deleteLeftoverError', { files: result.leftover });
            }
        } catch (e: any) {
            error = e.message || t('settings.extensions.deleteFailed');
        }
    }

    // Toggle enabled
    async function handleToggleEnabled(id: string, enabled: boolean) {
        error = '';
        try {
            await setExtensionEnabled(id, enabled);
            await loadExtensions();
        } catch (e: any) {
            error = e.message || t('settings.extensions.updateFailed');
        }
    }

    // The one directory a language server may read its resolved dependencies from. It lives in the Edit
    // dialog and rides that dialog's Save, because granting one stops the running sandbox: nothing should
    // happen until the dialog says save, and Cancel has to mean nothing changed. One draft and one error,
    // since only one dialog is open at a time.
    let editDepDraft = $state('');
    let editDepError = $state('');
    let homeDir = $state('');
    // Not state: nothing renders from either, so the effect below cannot re-trigger itself while it
    // reads and fills the draft.
    let homeTried = false;
    let draftFilled = false;

    /**
     * Where to suggest putting a dependency directory: the container path the extension declared,
     * mirrored under the server's own home. Language-blind by construction — it reads nothing but
     * the declaration and one path the server already reports at /api/home, never a probe — and for
     * the common case it lands on the directory the toolchain already uses.
     */
    function suggestedDepPath(ext: Extension): string {
        const declared = ext.sandbox_config?.dependency_cache;
        return declared && homeDir ? homeDir + declared.container_path : '';
    }

    /** What the dialog's field opens with: the granted directory, or the suggestion where none is. */
    function depDraftFor(ext: Extension): string {
        return ext.sandbox_config?.dependency_host_path || suggestedDepPath(ext);
    }

    $effect(() => {
        // Fetch the home directory once, and only if some installed extension declares a dependency
        // cache. The suggestion is editable text, never a value applied behind the user's back.
        const home = homeDir;
        if (!home) {
            if (!homeTried && extensions.some((e) => e.sandbox_config?.dependency_cache)) {
                homeTried = true;
                getHomeDirectory()
                    .then((h) => { homeDir = h; })
                    .catch(() => { /* a suggestion is a convenience; typing the path is the fallback */ });
            }
            return;
        }
        // The suggestion can land after the dialog opened, since it costs one fetch. It fills an empty
        // draft once and never overwrites what someone typed.
        if (!draftFilled && editingExtension && !editDepDraft) {
            draftFilled = true;
            editDepDraft = depDraftFor(editingExtension);
        }
    });

    // Check for updates
    async function handleCheckUpdates() {
        activeView = 'installed';
        loadingUpdates = true;
        error = '';
        try {
            updates = await checkUpdates();
            hasCheckedUpdates = true;
        } catch (e: any) {
            error = e.message || t('settings.extensions.checkUpdatesFailed');
        } finally {
            loadingUpdates = false;
        }
    }

    // Perform the real file/image replacement for one extension by streaming the
    // registry update operation. Returns true on success, false on failure.
    async function applyExtensionUpdate(id: string, name: string): Promise<boolean> {
        let ok = true;
        updating = new Set(updating).add(name);
        try {
            for await (const event of updateExtensionImage(id)) {
                setBuildProgress(event.extension, event.progress, event.message, event.error);
                if (event.done) {
                    if (event.error) {
                        error = event.error;
                        ok = false;
                        setTimeout(() => clearBuildProgress(event.extension), 6000);
                    } else {
                        setTimeout(() => clearBuildProgress(event.extension), 2000);
                    }
                }
            }
        } catch (e: any) {
            error = e.message || t('settings.extensions.updateFailed');
            ok = false;
        } finally {
            const next = new Set(updating);
            next.delete(name);
            updating = next;
        }
        return ok;
    }

    // Update a single extension — performs the actual file/image replacement.
    async function handleUpdate(update: ExtensionUpdate) {
        error = '';
        const ok = await applyExtensionUpdate(update.id, update.name);
        await loadExtensions();
        if (ok) {
            updates = updates.filter(u => u.id !== update.id);
        }
    }

    // Update all available extensions, sequentially, performing real replacements.
    async function handleUpdateAll() {
        updatingAll = true;
        error = '';
        try {
            for (const update of [...updates]) {
                const ok = await applyExtensionUpdate(update.id, update.name);
                if (ok) {
                    updates = updates.filter(u => u.id !== update.id);
                }
                // On failure the error is already set; continue with the rest.
            }
            await loadExtensions();
        } finally {
            updatingAll = false;
        }
    }

    // Update extension image from registry (rebuild or pull)
    async function handleUpdateImage(id: string, name: string) {
        const confirmed = await dialogStore.showConfirm({
            title: t('settings.extensions.updateImageTitle'),
            message: t('settings.extensions.updateImageMessage', { name }),
            confirmText: t('settings.extensions.update'),
        });
        if (!confirmed) return;

        error = '';
        const ok = await applyExtensionUpdate(id, name);
        await loadExtensions();
        if (ok) {
            updates = updates.filter(u => u.id !== id);
        }
    }

    // Open edit form
    function openEdit(ext: Extension) {
        editingExtension = ext;
        editExtensionId = ext.id;
        // For LSP extensions this seeds the host-owned memory_limit from the
        // effective sandbox_config value (0 = no limit).
        editConfig = initialEditConfig(ext);
        const lsp = ext.manifest.capabilities?.lsp;
        editLspSettings = {
            editor_enabled: lsp?.editor_enabled ?? true,
            tool_calls_enabled: lsp?.tool_calls_enabled ?? true,
        };
        editDepDraft = depDraftFor(ext);
        editDepError = '';
        // "Filled" means the draft holds the real answer rather than a placeholder waiting on the
        // home directory: a grant is final, and a suggestion needs no home to have been read from.
        draftFilled = !!ext.sandbox_config?.dependency_host_path || homeDir !== '';
    }

    // Close edit form
    function closeEdit() {
        editingExtension = null;
        editExtensionId = null;
        editConfig = {};
        editLspSettings = { editor_enabled: true, tool_calls_enabled: true };
        editDepDraft = '';
        editDepError = '';
        draftFilled = false;
    }

    // A memory limit input renders blank for "no limit" and parses blank/0/negative back to a numeric 0 (unlimited)
    function numberFieldValue(field: ConfigField): number | null {
        const raw = editConfig[field.key] ?? field.default;
        return field.key === HOST_OWNED_LSP_CONFIG_KEY ? memoryLimitToInput(raw) : raw;
    }

    function numberFieldInput(key: string, raw: string): number {
        return key === HOST_OWNED_LSP_CONFIG_KEY ? parseMemoryLimitMB(raw) : Number(raw);
    }

    // Save edit
    async function handleSave() {
        if (!editExtensionId) return;
        saving = true;
        error = '';
        editDepError = '';
        try {
            // The grant goes first: it is the change that stops a running sandbox, and a refused
            // directory must not close the dialog with the rest of the edits unsaid. Applying nothing
            // when the field still shows the stored path is what makes Cancel mean "no change".
            const stored = editingExtension?.sandbox_config?.dependency_host_path || '';
            const grant = editDepDraft.trim();
            if (editingExtension?.sandbox_config?.dependency_cache && grant !== stored) {
                try {
                    await setDependencyCache(editExtensionId, grant);
                    // Read back into the open dialog, so a retry of a save that failed later does not
                    // grant the same path twice, and the card's dot is right when the modal closes.
                    if (editingExtension.sandbox_config) editingExtension.sandbox_config.dependency_host_path = grant;
                } catch (e: any) {
                    editDepError = e.message || t('settings.extensions.depRejected');
                    return;
                }
            }
            const data: { config?: Record<string, any>; manifest?: Record<string, any> } = { config: editConfig };
            // Include LSP settings for LSP extensions
            if (editingExtension?.type === 'lsp') {
                data.manifest = {
                    capabilities: {
                        lsp: {
                            editor_enabled: editLspSettings.editor_enabled,
                            tool_calls_enabled: editLspSettings.tool_calls_enabled,
                        },
                    },
                };
            }
            await updateExtension(editExtensionId, data);
            closeEdit();
            await loadExtensions();
        } catch (e: any) {
            error = e.message || t('settings.extensions.saveFailed');
        } finally {
            saving = false;
        }
    }

    // Format built_at timestamp as a readable relative time
    function formatBuiltAt(builtAt: string): string {
        if (!builtAt) return t('settings.extensions.notBuilt');
        // 'calendar': a build from last March is a date, not an age.
        return timeAgo(builtAt, { scale: 'calendar', template: 'time.built' }) || t('time.built', { when: builtAt });
    }

    // Filter extensions by search, with local (unpublished) extensions first
    let filteredExtensions = $derived.by(() => {
        const list = searchQuery
            ? extensions.filter(ext =>
                ext.name.toLowerCase().includes(searchQuery.toLowerCase()) ||
                ext.manifest.display_name.toLowerCase().includes(searchQuery.toLowerCase()) ||
                ext.type.includes(searchQuery.toLowerCase())
            )
            : extensions;
        return [...list].sort((a, b) =>
            (b.install_source === 'local' ? 1 : 0) - (a.install_source === 'local' ? 1 : 0)
        );
    });

    // Local (filesystem-only) extensions not present in the registry, matched
    // by author/name so a local fork of a published extension (different
    // author namespace) is not hidden by a name-only collision.
    let unpublished = $derived.by(() => {
        const registryKeys = new Set(registryEntries.map(e => identityKey(e.author, e.name)));
        return localExtensions.filter(ext => !registryKeys.has(identityKey(ext.author, ext.name)));
    });

    // Identity keys of the unpublished extensions (template lookup).
    let unpublishedKeys = $derived(new Set(unpublished.map(e => identityKey(e.author, e.name))));

    // Identity keys of installed extensions (template lookup + browse pinning).
    let installedKeys = $derived(new Set(extensions.map(e => identityKey(e.author, e.name))));

    // Combined list for the browse view: unpublished (filesystem-only, not in
    // the registry) extensions at the top, then featured (not yet installed)
    // registry entries, then the remaining registry entries. See
    // orderBrowseEntries in $lib/composables/use-browse-order.
    let browseList = $derived.by(() =>
        orderBrowseEntries(
            unpublished,
            registryEntries,
            entry => installedKeys.has(identityKey(entry.author, entry.name)),
            searchQuery,
        )
    );

    // Initialize on mount
    $effect(() => {
        (async () => {
            await loadExtensions();
        })();
    });
</script>

<div class="extensions-tab">
    {#if error}
        <div class="error-banner" role="button" tabindex="0" onclick={() => error = ''} onkeydown={(e) => e.key === 'Enter' && (error = '')}>
            {error}
            <button class="dismiss" aria-label={t('settings.extensions.dismiss')}>&#x2715;</button>
        </div>
    {/if}

    <!-- Sticky header: view tabs + search -->
    <div class="sticky-header">
        <!-- View Tabs -->
        <div class="view-tabs">
            <div class="view-tabs-left">
                <button
                    class="view-tab"
                    class:active={activeView === 'installed'}
                    onclick={() => activeView = 'installed'}
                >
                    {t('settings.extensions.installedCount', { count: extensions.length })}
                </button>
                <button
                    class="view-tab"
                    class:active={activeView === 'browse'}
                    onclick={() => { activeView = 'browse'; loadLocalExtensions(); if (registryEntries.length === 0) loadRegistry(); }}
                >
                    {t('settings.extensions.browse')}
                </button>
            </div>
            <div class="view-tabs-right">
                <button class="btn btn-secondary" onclick={handleCheckUpdates} disabled={loadingUpdates}>
                    {loadingUpdates ? t('settings.extensions.checking') : t('settings.extensions.checkUpdates')}
                </button>
            </div>
        </div>

        <!-- Search -->
        <div class="search-row">
            <input
                type="search"
                placeholder={t('settings.extensions.searchPlaceholder')}
                value={searchQuery}
                oninput={(e) => searchQuery = (e.target as HTMLInputElement).value}
                class="search-input"
            />
        </div>
    </div>

    <!-- Installed View -->
    {#if activeView === 'installed'}
        <div class="installed-view">
            <!-- Updates Available -->
            {#if hasCheckedUpdates && updates.length === 0}
                <div class="updates-section updates-none">
                    <h3>{t('settings.extensions.noUpdates')}</h3>
                    <p class="updates-no-result">{t('settings.extensions.upToDate')}</p>
                </div>
            {:else if updates.length > 0}
                <div class="updates-section">
                    <div class="updates-header">
                        <h3>{t('settings.extensions.updatesAvailable')}</h3>
                        <button class="btn btn-primary btn-small" onclick={handleUpdateAll} disabled={updatingAll}>
                            {updatingAll ? t('settings.extensions.updating') : t('settings.extensions.updateAll')}
                        </button>
                    </div>
                    {#each updates as update}
                        {@const newPerms = update.new_permissions ?? []}
                        <div class="update-item" class:has-new-perms={newPerms.length > 0}>
                            <div class="update-info">
                                <span class="update-name" title={update.name}>{update.name}</span>
                                <span class="update-versions">{update.current_version} → {update.available_version}</span>
                            </div>
                            {#if (update.changelog ?? []).length > 0}
                                <div class="update-changelog">
                                    <div class="update-changelog-title">{t('settings.extensions.changesSince', { version: update.current_version })}</div>
                                    <ul class="update-changelog-list">
                                        {#each update.changelog as change}
                                            <li class="update-changelog-item">
                                                <span class="update-changelog-desc">{change.description}</span>
                                                {#if change.hash}<code class="update-changelog-hash" title={change.hash}>{change.hash.slice(0, 7)}</code>{/if}
                                            </li>
                                        {/each}
                                    </ul>
                                </div>
                            {/if}
                            {#if newPerms.length > 0}
                                <div class="update-new-perms">
                                    <div class="update-new-perms-title">{t('settings.extensions.newPermsTitle')}</div>
                                    <ul class="update-new-perms-list">
                                        {#each sortPermissions(newPerms) as perm}
                                            {@const info = PERMISSION_DESCRIPTIONS[perm]}
                                            <li class="update-new-perm">
                                                <span class="update-new-perm-name">
                                                    {info?.label || perm}
                                                    {#if isDangerousPermission(perm)}
                                                        <svg class="perm-danger-icon" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" role="img" aria-label={t('settings.extensions.permDanger')}><title>{t('settings.extensions.permDanger')}</title><path d="m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 20h16a2 2 0 0 0 1.73-2Z"/><path d="M12 9v4"/><path d="M12 17h.01"/></svg>
                                                    {/if}
                                                </span>
                                                {#if info?.description}<span class="update-new-perm-desc">{info.description}</span>{/if}
                                            </li>
                                        {/each}
                                    </ul>
                                </div>
                            {/if}
                            <button class="btn btn-primary btn-small update-action" onclick={() => handleUpdate(update)}>{t('settings.extensions.update')}</button>
                        </div>
                    {/each}
                </div>
            {/if}

            <!-- Extension List -->
            {#if loading}
                <div class="loading">{t('settings.extensions.loadingExtensions')}</div>
            {:else if filteredExtensions.length === 0}
                <div class="empty-state">
                    <p>{t('settings.extensions.noneInstalled')}</p>
                    <p class="help">{t('settings.extensions.noneInstalledHelp')}</p>
                    <button class="btn btn-primary" onclick={() => { activeView = 'browse'; loadLocalExtensions(); loadRegistry(); }}>
                        {t('settings.extensions.browse')}
                    </button>
                </div>
            {:else}
                <div class="extension-list">
                    {#each filteredExtensions as ext}
                        {@const isLocal = ext.install_source === 'local'}
                        <div class="extension-card" class:disabled={!ext.enabled} class:unpublished-card={isLocal}>
                            <div class="card-header">
                                <div class="ext-icon" class:ext-lsp={ext.type === 'lsp'} class:ext-ui={ext.type === 'ui'} class:ext-css={ext.type === 'css'} class:ext-agent={ext.type === 'agent'} class:ext-llm={ext.type === 'llm'} class:ext-i18n={ext.type === 'i18n'}>
                                    {typeBadge(ext.type)}
                                </div>
                                <div class="ext-info">
                                    <div class="ext-name-row">
                                        <span class="ext-name">{ext.manifest.display_name || ext.name}</span>
                                        {#if isLocal}
                                            <button
                                                class="unpublished-badge"
                                                type="button"
                                                title={t('settings.extensions.localInstalledTitle')}
                                                aria-expanded={revealedLocalPath === ext.id}
                                                onclick={(e) => { e.stopPropagation(); revealedLocalPath = revealedLocalPath === ext.id ? null : ext.id; }}
                                            >{t('settings.extensions.local')}</button>
                                        {/if}
                                        {#if needsDependencyGrant(ext)}
                                            <!-- Same attention color as the dot on the Edit button, and it opens
                                                  the same dialog: one state, said twice because a dot on an icon
                                                  is easy to look straight past. -->
                                            <button
                                                class="needs-setup-badge"
                                                type="button"
                                                title={t('settings.extensions.needsSetupTitle')}
                                                onclick={(e) => { e.stopPropagation(); openEdit(ext); }}
                                            >{t('settings.extensions.needsSetup')}</button>
                                        {/if}
                                        {#if getCapabilityKeys(ext).length > 0}
                                            <div class="capability-badges" role="group" aria-label={t('settings.extensions.capabilitiesGroup')}>
                                                {#each getCapabilityKeys(ext) as capKey}
                                                    <button class="cap-badge" class:cap-lsp={capKey === 'lsp'} class:cap-ui={capKey === 'ui'} class:cap-css={capKey === 'css'} class:cap-agent={capKey === 'agent'} class:cap-network={capKey === 'network'} class:cap-llm={capKey === 'llm'} class:cap-i18n={capKey === 'i18n'} aria-label={t('settings.extensions.capAria', { label: CAPABILITY_DESCRIPTIONS[capKey]?.label || capKey, name: ext.manifest.display_name || ext.name })} onclick={() => { selectedExtension = ext; showCapabilitiesModal = true; }}>
                                                        {CAPABILITY_DESCRIPTIONS[capKey]?.label || capKey}
                                                    </button>
                                                {/each}
                                            </div>
                                        {/if}
                                    </div>
                                    <span class="ext-meta">
                                        {#if ext.type === 'ui' || ext.type === 'css'}
                                            v{ext.version}
                                        {:else}
                                            {formatBuiltAt(ext.built_at)}
                                        {/if}
                                        {#if ext.manifest.author}
                                            · {ext.manifest.author}
                                        {/if}
                                        {#if ext.manifest.license_url} · <a href={safeUrl(ext.manifest.license_url)} target="_blank" rel="noopener noreferrer" class="ext-link">{t('settings.extensions.license')}</a>{/if}
                                        {#if ext.manifest.homepage} · <a href={safeUrl(ext.manifest.homepage)} target="_blank" rel="noopener noreferrer" class="ext-link">{t('settings.extensions.website')}</a>{/if}
                                        {#if ext.type === 'i18n' && packLocale(ext.manifest.capabilities)} · <span class="ext-locale" title={t('settings.extensions.localeHint')}>{packLocale(ext.manifest.capabilities)}</span>{/if}
                                    </span>
                                </div>
                                <div class="card-actions">
                                    <button class="btn btn-icon btn-danger" title={t('settings.extensions.delete')} onclick={() => handleDelete(ext.id, ext.name)}>
                                        <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                                            <path d="M2 4h12M5 4V3a1 1 0 011-1h4a1 1 0 011 1v1M6 7v5M10 7v5" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                                            <path d="M3 4l1 9a1 1 0 001 1h6a1 1 0 001-1l1-9" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                                        </svg>
                                    </button>
                                    {#if ext.type === 'lsp'}
                                    <button class="btn btn-icon" title={t('settings.extensions.updateImageTitle')} onclick={() => handleUpdateImage(ext.id, ext.name)} disabled={updating.has(ext.name)}>
                                        {#if updating.has(ext.name)}
                                            <span class="spinner"></span>
                                        {:else}
                                            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                                                <path d="M21 2v6h-6"/>
                                                <path d="M3 12a9 9 0 0 1 15-6.7L21 8"/>
                                                <path d="M3 22v-6h6"/>
                                                <path d="M21 12a9 9 0 0 1-15 6.7L3 16"/>
                                            </svg>
                                        {/if}
                                    </button>
                                    {/if}
                                    {#if ext.manifest.config_schema || ext.type === 'lsp'}
                                    {@const needsDep = needsDependencyGrant(ext)}
                                    <button
                                        class="btn btn-icon"
                                        title={needsDep ? t('settings.extensions.editNeedsSetup') : t('settings.extensions.edit')}
                                        aria-label={needsDep ? t('settings.extensions.editNeedsSetup') : t('settings.extensions.edit')}
                                        onclick={() => openEdit(ext)}
                                    >
                                        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                                            <path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"/>
                                            <path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"/>
                                        </svg>
                                        {#if needsDep}
                                            <span class="ext-needs-dot" aria-hidden="true"></span>
                                        {/if}
                                    </button>
                                    {/if}
                                    <ToggleSwitch class="card-toggle" checked={ext.enabled} onChange={() => handleToggleEnabled(ext.id, !ext.enabled)} />
                                </div>
                            </div>
                            {#if ext.environment && !ext.environment.healthy}
                                <!-- One honest sentence for the state behind the diagnostics core
                                     withheld: the server could not resolve this project, so its
                                     "package not found" answers were never facts about the code. -->
                                <div class="ext-env-note" role="status">
                                    <span class="ext-env-note-text">{ext.environment.note || t('settings.extensions.envNote')}</span>
                                </div>
                            {/if}
                            <!-- The dependency directory is configured in the Edit dialog, not on the card:
                                 granting one stops the running sandbox, so it belongs behind the same Save as
                                 the rest of the settings. What the card carries is the state — the badge above
                                 and the dot on Edit, both from needsDependencyGrant(ext). -->
                            {#if buildProgress[ext.name]}
                                {@const bp = buildProgress[ext.name]}
                                <div class="ext-build" class:ext-build-error={!!bp.error}>
                                    <div class="ext-build-bar"><div class="ext-build-fill" style="--pct: {bp.progress}%"></div></div>
                                    <span class="ext-build-text">{bp.error || bp.message}</span>
                                </div>
                            {/if}
                            {#if isLocal && revealedLocalPath === ext.id}
                                {@const p = ext.local_path}
                                <div class="local-path">
                                    <span class="local-path-label">{t('settings.extensions.path')}</span>
                                    {#if p}
                                        <code class="local-path-code" title={p}>{p}</code>
                                    {:else}
                                        <span class="local-path-unknown">{t('settings.extensions.pathUnavailable')}</span>
                                    {/if}
                                </div>
                            {/if}
                            {#if ext.manifest.description}
                                <p class="ext-description">{ext.manifest.description}</p>
                            {/if}
                            {#if ext.manifest.capabilities?.network?.allowed_domains?.length}
                                <div class="ext-proxy-domains">
                                    <span class="ext-proxy-label">{t('settings.extensions.connectsTo')}</span>
                                    <div class="cap-detail-tags">
                                        {#each ext.manifest.capabilities.network.allowed_domains as domain}
                                            <button class="cap-detail-tag" onclick={() => { selectedExtension = ext; showCapabilitiesModal = true; }}>{domain}</button>
                                        {/each}
                                    </div>
                                </div>
                            {/if}
                            {#if ext.type === 'agent'}
                                <p class="ext-note">
                                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="12" r="10"/><line x1="12" y1="16" x2="12" y2="12"/><line x1="12" y1="8" x2="12.01" y2="8"/></svg>
                                    {t('settings.extensions.agentNote.before')} {ext.manifest.display_name} {t('settings.extensions.agentNote.after')} <code>{t('settings.tab.providers')}</code>
                                </p>
                            {/if}
                        </div>
                    {/each}
                </div>
            {/if}
        </div>
    {/if}

    <!-- Edit Extension Modal -->
    {#if editingExtension}
        {#snippet editFooter()}
            <button type="button" class="btn-secondary" onclick={closeEdit}>{t('settings.cancel')}</button>
            <button type="button" class="btn-primary" onclick={handleSave} disabled={saving}>
                {saving ? t('settings.saving') : t('settings.save')}
            </button>
        {/snippet}

        <BaseModal title={t('settings.extensions.editTitle', { name: editingExtension.manifest.display_name || editingExtension.name })} width="480px" footer={editFooter} onClose={closeEdit}>
            <!-- Dependencies first: it is the one field that decides whether this server can resolve
                 anything. It rides this dialog's Save rather than carrying its own Apply, so Cancel means
                 nothing changed — the grant lands here, and applying it stops the running sandbox. -->
            {#if editingExtension.sandbox_config?.dependency_cache}
                {@const dep = editingExtension.sandbox_config.dependency_cache}
                {@const stored = editingExtension.sandbox_config.dependency_host_path || ''}
                {@const draft = editDepDraft.trim()}
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
                            aria-label={t('settings.extensions.depAria', { name: editingExtension.name })}
                            placeholder="/path/to/dependency/directory"
                            bind:value={editDepDraft}
                            disabled={saving}
                        />
                        <div class="ext-dep-state">
                            {#if stored}
                                <span class="ext-dep-state-label">{t('settings.extensions.depMounted')}</span>
                                <code class="ext-dep-path" title={t('settings.extensions.depMountedTitle', { host: stored, container: dep.container_path })}>{stored}</code>
                                <button type="button" class="btn-secondary btn-small" onclick={() => { editDepDraft = ''; }} disabled={saving}
                                    title={t('settings.extensions.depUnmountHint')}>{t('settings.extensions.depUnmount')}</button>
                            {:else}
                                <span class="ext-dep-none">{t('settings.extensions.depNone')}</span>
                            {/if}
                            {#if draft !== stored}
                                <span class="ext-dep-pending">{t('settings.extensions.depPending', { action: draft === '' ? t('settings.extensions.depPendingUnmount') : stored ? t('settings.extensions.depPendingReplace') : t('settings.extensions.depPendingMount') })}</span>
                            {/if}
                        </div>
                        {#if editDepError}
                            <p class="ext-dep-error" role="alert">{editDepError}</p>
                        {/if}
                    </div>
                </div>
            {/if}
            <!-- Config Schema Fields -->
            {#if effectiveConfigFields(editingExtension).length > 0}
                <div class="config-fields">
                    <h4>{t('settings.extensions.configuration')}</h4>
                    {#each effectiveConfigFields(editingExtension) as field}
                        <div class="config-field">
                            <label class="field-label" for="edit-config-{field.key}">{field.title}</label>
                            {#if field.description}
                                <p class="field-desc">{field.description}</p>
                            {/if}
                            {#if field.type === 'mount'}
                                {@const mounts = (editConfig[field.key] ?? field.default ?? []) as SandboxMount[]}
                                <div class="mount-editor">
                                    {#if mounts.length > 0}
                                        <div class="mount-list">
                                            {#each mounts as mount, i}
                                                <div class="mount-row">
                                                    <input type="text" class="mount-input" value={mount.host_path} placeholder="/host/path" oninput={(e) => { const updated = [...mounts]; updated[i] = { ...mount, host_path: (e.target as HTMLInputElement).value }; editConfig[field.key] = updated; }} />
                                                    <svg width="12" height="12" viewBox="0 0 12 12" fill="none" class="mount-arrow"><path d="M4 3L8 6L4 9" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/></svg>
                                                    <input type="text" class="mount-input" value={mount.container_path} placeholder="/container/path" oninput={(e) => { const updated = [...mounts]; updated[i] = { ...mount, container_path: (e.target as HTMLInputElement).value }; editConfig[field.key] = updated; }} />
                                                    <div class="radio-group radio-group-small">
                                                        <label class="radio-label" class:active={mount.mode === 'rw'}><input type="radio" name={`mount-mode-${i}`} value="rw" checked={mount.mode === 'rw'} onchange={() => { const updated = [...mounts]; updated[i] = { ...mount, mode: 'rw' }; editConfig[field.key] = updated; }}/><span class="radio-text">rw</span></label>
                                                        <label class="radio-label" class:active={mount.mode === 'ro'}><input type="radio" name={`mount-mode-${i}`} value="ro" checked={mount.mode === 'ro'} onchange={() => { const updated = [...mounts]; updated[i] = { ...mount, mode: 'ro' }; editConfig[field.key] = updated; }}/><span class="radio-text">ro</span></label>
                                                    </div>
                                                    <button type="button" class="btn-remove" onclick={() => { editConfig[field.key] = mounts.filter((_, j) => j !== i); }} title={t('settings.extensions.remove')}><svg width="12" height="12" viewBox="0 0 12 12" fill="none"><path d="M3 3L9 9M9 3L3 9" stroke="currentColor" stroke-width="1.3" stroke-linecap="round"/></svg></button>
                                                </div>
                                            {/each}
                                        </div>
                                    {/if}
                                    <button type="button" class="btn-add" onclick={() => { editConfig[field.key] = [...mounts, { host_path: '', container_path: '', mode: 'rw' as const }]; }} title={t('settings.extensions.addMount')}><svg width="14" height="14" viewBox="0 0 14 14" fill="none"><path d="M7 2V12M2 7H12" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/></svg></button>
                                </div>
                            {:else if field.type === 'env'}
                                {@const envs = (editConfig[field.key] ?? field.default ?? []) as SandboxEnv[]}
                                <div class="env-editor">
                                    {#if envs.length > 0}
                                        <div class="env-list">
                                            {#each envs as env, i}
                                                <div class="env-row">
                                                    <input type="text" class="env-input env-input-key" value={env.key} placeholder="KEY" oninput={(e) => { const updated = [...envs]; updated[i] = { ...env, key: (e.target as HTMLInputElement).value }; editConfig[field.key] = updated; }} />
                                                    <span class="env-equals">=</span>
                                                    <input type="text" class="env-input" value={env.value ?? ''} placeholder="value" oninput={(e) => { const updated = [...envs]; updated[i] = { ...env, value: (e.target as HTMLInputElement).value }; editConfig[field.key] = updated; }} />
                                                    <button type="button" class="btn-remove" onclick={() => { editConfig[field.key] = envs.filter((_, j) => j !== i); }} title={t('settings.extensions.remove')}><svg width="12" height="12" viewBox="0 0 12 12" fill="none"><path d="M3 3L9 9M9 3L3 9" stroke="currentColor" stroke-width="1.3" stroke-linecap="round"/></svg></button>
                                                </div>
                                            {/each}
                                        </div>
                                    {/if}
                                    <button type="button" class="btn-add" onclick={() => { editConfig[field.key] = [...envs, { key: '', value: '' }]; }} title={t('settings.extensions.addVariable')}><svg width="14" height="14" viewBox="0 0 14 14" fill="none"><path d="M7 2V12M2 7H12" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/></svg></button>
                                </div>
                            {:else if field.type === 'number'}
                                <input
                                    id="edit-config-{field.key}"
                                    type="number"
                                    value={numberFieldValue(field)}
                                    placeholder={field.key === HOST_OWNED_LSP_CONFIG_KEY ? t('settings.extensions.noLimit') : undefined}
                                    oninput={(e) => editConfig[field.key] = numberFieldInput(field.key, (e.target as HTMLInputElement).value)}
                                    class="config-input"
                                />
                            {:else if field.type === 'boolean'}
                                <label class="toggle-label">
                                    <ToggleSwitch
                                        checked={editConfig[field.key] ?? field.default ?? false}
                                        onChange={() => editConfig[field.key] = !(editConfig[field.key] ?? field.default ?? false)}
                                    />
                                </label>
                            {:else}
                                <input
                                    id="edit-config-{field.key}"
                                    type={field.sensitive ? 'password' : 'text'}
                                    value={editConfig[field.key] ?? field.default ?? ''}
                                    oninput={(e) => editConfig[field.key] = (e.target as HTMLInputElement).value}
                                    class="config-input"
                                />
                            {/if}
                        </div>
                    {/each}
                </div>
            {:else if editingExtension.type !== 'lsp'}
                <p class="no-schema">{t('settings.extensions.noSchema')}</p>
            {/if}
            <!-- Sandbox (host-owned) — every LSP extension runs in a sandbox, so
                 the memory limit is always available, declared or not. -->
            {#if editingExtension?.type === 'lsp'}
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
                            value={memoryLimitToInput(editConfig.memory_limit)}
                            oninput={(e) => editConfig.memory_limit = parseMemoryLimitMB((e.target as HTMLInputElement).value)}
                            class="config-input"
                        />
                    </div>
                </div>
            {/if}
            <!-- LSP Settings -->
            {#if editingExtension?.type === 'lsp'}
                <div class="config-fields">
                    <h4>{t('settings.extensions.lspFeatures')}</h4>
                    <div class="config-field">
                        <span class="field-label">{t('settings.extensions.editorFeatures')}</span>
                        <p class="field-desc">{t('settings.extensions.editorFeaturesDesc')}</p>
                        <ToggleSwitch
                            checked={editLspSettings.editor_enabled}
                            onChange={() => editLspSettings.editor_enabled = !editLspSettings.editor_enabled}
                            ariaLabel={t('settings.extensions.editorFeaturesAria')}
                        />
                    </div>
                    <div class="config-field">
                        <span class="field-label">{t('settings.extensions.toolCalls')}</span>
                        <p class="field-desc">{t('settings.extensions.toolCallsDesc')}</p>
                        <ToggleSwitch
                            checked={editLspSettings.tool_calls_enabled}
                            onChange={() => editLspSettings.tool_calls_enabled = !editLspSettings.tool_calls_enabled}
                            ariaLabel={t('settings.extensions.toolCallsAria')}
                        />
                    </div>
                </div>
            {/if}
        </BaseModal>
    {/if}

    <!-- Capabilities Modal -->
    {#if showCapabilitiesModal && selectedExtension}
        {#snippet capsFooter()}
            <button type="button" class="btn-secondary" onclick={() => { showCapabilitiesModal = false; selectedExtension = null; }}>{t('settings.close')}</button>
        {/snippet}

        <BaseModal title={t('settings.extensions.capsTitle')} width="520px" footer={capsFooter} onClose={() => { showCapabilitiesModal = false; selectedExtension = null; }}>
            <div class="caps-modal-body">
                <div class="caps-ext-header">
                    <div class="caps-ext-icon" class:ext-lsp={selectedExtension.type === 'lsp'} class:ext-ui={selectedExtension.type === 'ui'} class:ext-css={selectedExtension.type === 'css'} class:ext-agent={selectedExtension.type === 'agent'} class:ext-llm={selectedExtension.type === 'llm'} class:ext-i18n={selectedExtension.type === 'i18n'} aria-hidden="true">
                        {typeBadge(selectedExtension.type)}
                    </div>
                    <div class="caps-ext-info">
                        <h3 class="caps-ext-name">{selectedExtension.manifest.display_name || selectedExtension.name}</h3>
                        {#if selectedExtension.manifest.description}
                            <p class="caps-ext-desc">{selectedExtension.manifest.description}</p>
                        {/if}
                    </div>
                </div>

                <div class="caps-list" role="list" aria-label={t('settings.extensions.capsDetailsAria')}>
                    {#each getCapabilityKeys(selectedExtension) as capKey}
                        {@const info = CAPABILITY_DESCRIPTIONS[capKey]}
                        {@const caps = selectedExtension.manifest.capabilities}
                        {#if info}
                            <div class="cap-detail" role="listitem">
                                <div class="cap-detail-header">
                                    <span class="cap-detail-badge" class:cap-lsp={capKey === 'lsp'} class:cap-ui={capKey === 'ui'} class:cap-css={capKey === 'css'} class:cap-agent={capKey === 'agent'} class:cap-network={capKey === 'network'} class:cap-llm={capKey === 'llm'} class:cap-i18n={capKey === 'i18n'}>
                                        {info.label}
                                    </span>
                                </div>
                                <p class="cap-detail-desc">{info.description}</p>
                                <!-- Network: show allowed domains -->
                                {#if capKey === 'network' && caps?.network?.allowed_domains?.length}
                                    <div class="cap-detail-extra">
                                        <span class="cap-detail-label">{t('settings.extensions.allowedDomains')}</span>
                                        <div class="cap-detail-tags">
                                            {#each caps.network.allowed_domains as domain}
                                                <span class="cap-detail-tag">{domain}</span>
                                            {/each}
                                        </div>
                                    </div>
                                {/if}
                                <!-- UI: show injection points -->
                                {#if capKey === 'ui' && caps?.ui?.injection_points?.length}
                                    <div class="cap-detail-extra">
                                        <span class="cap-detail-label">{t('settings.extensions.modifies')}</span>
                                        <div class="cap-detail-tags">
                                            {#each caps.ui.injection_points as point}
                                                <span class="cap-detail-tag">{INJECTION_POINT_LABELS[point] || point}</span>
                                            {/each}
                                        </div>
                                    </div>
                                {/if}
                                <!-- UI: show declared permissions -->
                                {#if capKey === 'ui' && caps?.ui?.permissions?.length}
                                    <div class="cap-detail-extra cap-detail-extra-block">
                                        <span class="cap-detail-label">{t('settings.extensions.permissions')}</span>
                                        <div class="cap-permission-list">
                                            {#each sortPermissions(caps.ui.permissions) as perm}
                                                {@const info = PERMISSION_DESCRIPTIONS[perm]}
                                                <div class="cap-permission">
                                                    <span class="cap-permission-name">
                                                        {info?.label || perm}
                                                        {#if isDangerousPermission(perm)}
                                                            <svg class="perm-danger-icon" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" role="img" aria-label={t('settings.extensions.permDanger')}><title>{t('settings.extensions.permDanger')}</title><path d="m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 20h16a2 2 0 0 0 1.73-2Z"/><path d="M12 9v4"/><path d="M12 17h.01"/></svg>
                                                        {/if}
                                                    </span>
                                                    {#if info?.description}
                                                        <span class="cap-permission-desc">{info.description}</span>
                                                    {:else}
                                                        <span class="cap-permission-desc">{t('settings.extensions.permUndocumented')}</span>
                                                    {/if}
                                                </div>
                                            {/each}
                                        </div>
                                    </div>
                                {/if}
                                <!-- LSP: show language and extensions -->
                                {#if capKey === 'lsp' && caps?.lsp}
                                    <div class="cap-detail-extra">
                                        {#if caps.lsp.language}
                                            <span class="cap-detail-label">{t('settings.extensions.language')}</span>
                                            <span class="cap-detail-tags">
                                                <span class="cap-detail-tag">{caps.lsp.language}</span>
                                            </span>
                                        {/if}
                                        {#if caps.lsp.file_types?.length}
                                            <span class="cap-detail-label">{t('settings.extensions.fileTypes')}</span>
                                            <div class="cap-detail-tags">
                                                {#each caps.lsp.file_types as ft (ft.extension)}
                                                    <!-- The tooltip names the language the server is told about this file type. -->
                                                    <span class="cap-detail-tag" title={ft.label ? `${ft.label} · ${ft.language_id}` : ft.language_id}>
                                                        .{ft.extension}
                                                    </span>
                                                {/each}
                                            </div>
                                        {/if}
                                    </div>
                                {/if}
                                <!-- CSS: show stylesheet URL -->
                                {#if capKey === 'css' && caps?.css?.css_url}
                                    <div class="cap-detail-extra">
                                        <span class="cap-detail-label">{t('settings.extensions.stylesheet')}</span>
                                        <code class="cap-detail-value">{caps.css.css_url}</code>
                                    </div>
                                {/if}
                                <!-- Translation pack: the locale tag it declares, verbatim —
                                     core holds no language names to render. -->
                                {#if capKey === 'i18n' && packLocale(caps)}
                                    <div class="cap-detail-extra">
                                        <span class="cap-detail-label">{t('settings.extensions.language')}</span>
                                        <code class="cap-detail-value">{packLocale(caps)}</code>                                    </div>
                                {/if}
                            </div>
                        {/if}
                    {/each}
                </div>
            </div>
        </BaseModal>
    {/if}

    <!-- Install Confirm: shown before installing an extension that declares
         the network capability or sandbox-escaping permissions; the user
         reviews the network capability (allowed domains) and the full declared
         permission list before the install starts. -->
    {#if pendingInstall}
        {#snippet installConfirmFooter()}
            <button type="button" class="btn-secondary" onclick={() => pendingInstall = null}>{t('settings.cancel')}</button>
            <button type="button" class="btn-primary" onclick={confirmPendingInstall}>{t('settings.extensions.install')}</button>
        {/snippet}

        {@const pendingPerms = sortPermissions((pendingInstall.entry.capabilities?.ui?.permissions ?? []) as string[])}
        {@const pendingNetwork = pendingInstall.entry.capabilities?.network}
        {@const pendingDep = dependencyCacheDeclaration(pendingInstall.entry.capabilities)}
        <BaseModal
            title={t('settings.extensions.installTitle', { name: pendingInstall.entry.display_name || pendingInstall.entry.name })}
            width="480px"
            modalClass="install-confirm-modal"
            footer={installConfirmFooter}
            onClose={() => { pendingInstall = null; }}
        >
            <p class="install-confirm-intro">{t('settings.extensions.installIntro')}</p>
            <div class="cap-permission-list">
                {#if pendingNetwork}
                    <div class="cap-permission">
                        <span class="cap-permission-name">
                            {CAPABILITY_DESCRIPTIONS.network.label}
                            <svg class="perm-danger-icon" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" role="img" aria-label={t('settings.extensions.permDanger')}><title>{t('settings.extensions.permDanger')}</title><path d="m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 20h16a2 2 0 0 0 1.73-2Z"/><path d="M12 9v4"/><path d="M12 17h.01"/></svg>
                        </span>
                        <span class="cap-permission-desc">{CAPABILITY_DESCRIPTIONS.network.description}</span>
                        {#if pendingNetwork.allowed_domains?.length}
                            <div class="cap-detail-tags">
                                {#each pendingNetwork.allowed_domains as domain}
                                    <span class="cap-detail-tag">{domain}</span>
                                {/each}
                            </div>
                        {/if}
                    </div>
                {/if}
                {#if pendingDep}
                    <!-- Not a sandbox escape and not labelled as one: installing grants nothing.
                         What it does is mean a folder on this machine gets asked about afterwards,
                         which is the part worth saying before anyone clicks Install. -->
                    <div class="cap-permission">
                        <span class="cap-permission-name">{t('settings.extensions.depPermission')}</span>
                        <span class="cap-permission-desc">
                            {t('settings.extensions.depPermissionDesc.before')} <code>{pendingDep.env}={pendingDep.container_path}</code> {t('settings.extensions.depPermissionDesc.after')}
                        </span>
                    </div>
                {/if}
                {#each pendingPerms as perm}
                    {@const info = PERMISSION_DESCRIPTIONS[perm]}
                    <div class="cap-permission">
                        <span class="cap-permission-name">
                            {info?.label || perm}
                            {#if isDangerousPermission(perm)}
                                <svg class="perm-danger-icon" width="12" height="12" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" role="img" aria-label={t('settings.extensions.permDanger')}><title>{t('settings.extensions.permDanger')}</title><path d="m21.73 18-8-14a2 2 0 0 0-3.48 0l-8 14A2 2 0 0 0 4 20h16a2 2 0 0 0 1.73-2Z"/><path d="M12 9v4"/><path d="M12 17h.01"/></svg>
                            {/if}
                        </span>
                        {#if info?.description}
                            <span class="cap-permission-desc">{info.description}</span>
                        {:else}
                            <span class="cap-permission-desc">{t('settings.extensions.permUndocumented')}</span>
                        {/if}
                    </div>
                {/each}
            </div>
        </BaseModal>
    {/if}

    <!-- Browse View -->
    {#if activeView === 'browse'}
        <div class="browse-view">

            <!-- Registry List -->
            {#if loadingRegistry}
                <div class="loading">{t('settings.extensions.loadingRegistry')}</div>
            {:else if browseList.length === 0}
                <div class="empty-state">
                    <p>{t('settings.extensions.registryEmpty')}</p>
                    <p class="help">{t('settings.extensions.registryEmptyHelp')}</p>
                </div>
            {:else}
                <div class="extension-list">
                    {#each browseList as entry}
                        {@const regCapKeys = getRegistryCapabilityKeys(entry)}
                        {@const isUnpublished = unpublishedKeys.has(identityKey(entry.author, entry.name))}
                        {@const isInstalled = installedKeys.has(identityKey(entry.author, entry.name))}
                        {@const needsSetup = requiresSetupAfterInstall(entry.capabilities)}
                        {@const setupDep = dependencyCacheDeclaration(entry.capabilities)}
                        <div class="extension-card registry-card" class:unpublished-card={isUnpublished}>
                            <div class="card-header">
                                <div class="ext-icon" class:ext-lsp={entry.type === 'lsp'} class:ext-ui={entry.type === 'ui'} class:ext-css={entry.type === 'css'} class:ext-agent={entry.type === 'agent'} class:ext-llm={entry.type === 'llm'} class:ext-i18n={entry.type === 'i18n'}>
                                    {typeBadge(entry.type)}
                                </div>
                                <div class="ext-info">
                                    <div class="ext-name-row">
                                        <span class="ext-name">{entry.display_name || entry.name}</span>
                                        {#if entry.featured}
                                            <span class="featured-badge">{t('settings.extensions.featured')}</span>
                                        {/if}
                                        {#if isUnpublished}
                                            <button
                                                class="unpublished-badge"
                                                type="button"
                                                title={t('settings.extensions.localUnpublishedTitle')}
                                                aria-expanded={revealedLocalPath === identityKey(entry.author, entry.name)}
                                                onclick={(e) => { e.stopPropagation(); const k = identityKey(entry.author, entry.name); revealedLocalPath = revealedLocalPath === k ? null : k; }}
                                            >{t('settings.extensions.local')}</button>
                                        {/if}
                                        {#if needsSetup}
                                            <!-- A plain label, not a button: nothing is installed yet, so there is no record to open. -->
                                            <span
                                                class="needs-setup-badge"
                                                title={t('settings.extensions.needsSetupRegistryTitle', { env: setupDep?.env, container: setupDep?.container_path })}
                                            >{t('settings.extensions.needsSetup')}</span>
                                        {/if}
                                        {#if regCapKeys.length > 0}
                                            <div class="capability-badges" role="group" aria-label={t('settings.extensions.capabilitiesGroup')}>
                                                {#each regCapKeys as regCapKey}
                                                    {@const regCapInfo = CAPABILITY_DESCRIPTIONS[regCapKey]}
                                                    <button class="cap-badge" class:cap-lsp={regCapKey === 'lsp'} class:cap-ui={regCapKey === 'ui'} class:cap-css={regCapKey === 'css'} class:cap-agent={regCapKey === 'agent'} class:cap-network={regCapKey === 'network'} class:cap-llm={regCapKey === 'llm'} class:cap-i18n={regCapKey === 'i18n'} aria-label={t('settings.extensions.capAriaFull', { label: regCapInfo.label, name: entry.display_name || entry.name, description: regCapInfo.description })} onclick={() => { selectedExtension = { name: entry.name, manifest: { display_name: entry.display_name, description: entry.description, capabilities: getRegistryCapabilities(entry) }, type: entry.type } as Extension; showCapabilitiesModal = true; }}>
                                                        {regCapInfo.label}
                                                    </button>
                                                {/each}
                                            </div>
                                        {/if}
                                    </div>
                                    <span class="ext-meta">
                                        v{entry.version}
                                        {#if entry.author}
                                            · {entry.author}
                                        {/if}
                                        {#if entry.license_url} · <a href={safeUrl(entry.license_url)} target="_blank" rel="noopener noreferrer" class="ext-link">{t('settings.extensions.license')}</a>{/if}
                                        {#if entry.homepage} · <a href={safeUrl(entry.homepage)} target="_blank" rel="noopener noreferrer" class="ext-link">{t('settings.extensions.website')}</a>{/if}
                                        <!-- The locale an i18n pack declares, shown in its own language: the one thing a
                                             person needs to pick the right pack out of the list. -->
                                        {#if entry.type === 'i18n' && packLocale(entry.capabilities)} · <span class="ext-locale" title={t('settings.extensions.localeHint')}>{packLocale(entry.capabilities)}</span>{/if}
                                    </span>
                                </div>
                                <div class="ext-action">
                                    {#if isInstalled}
                                        <span class="installed-badge">{t('settings.extensions.installed')}</span>
                                    {:else}
                                        <button class="btn btn-primary btn-small" onclick={() => handleInstall(entry, isUnpublished)} disabled={installing.has(entry.name)}>
                                            {#if installing.has(entry.name)}
                                                <span class="spinner"></span>
                                                {t('settings.extensions.installingNow')}
                                            {:else}
                                                {t('settings.extensions.install')}
                                            {/if}
                                        </button>
                                    {/if}
                                </div>
                            </div>
                            {#if buildProgress[entry.name]}
                                {@const bp = buildProgress[entry.name]}
                                <div class="ext-build" class:ext-build-error={!!bp.error}>
                                    <div class="ext-build-bar"><div class="ext-build-fill" style="--pct: {bp.progress}%"></div></div>
                                    <span class="ext-build-text">{bp.error || bp.message}</span>
                                </div>
                            {/if}
                            {#if isUnpublished && revealedLocalPath === identityKey(entry.author, entry.name)}
                                {@const p = entry.local_path}
                                <div class="local-path">
                                    <span class="local-path-label">{t('settings.extensions.path')}</span>
                                    {#if p}
                                        <code class="local-path-code" title={p}>{p}</code>
                                    {:else}
                                        <span class="local-path-unknown">{t('settings.extensions.pathUnavailable')}</span>
                                    {/if}
                                </div>
                            {/if}
                            {#if entry.description}
                                <p class="ext-description">{entry.description}</p>
                            {/if}
                            {#if needsSetup}
                                <!-- Not a permission warning: installing grants nothing. The step is a folder to pick. -->
                                <p class="ext-note ext-needs-setup-note">
                                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="12" r="10"/><line x1="12" y1="16" x2="12" y2="12"/><line x1="12" y1="8" x2="12.01" y2="8"/></svg>
                                    <span>{t('settings.extensions.needsSetupNote.before')} <code>{setupDep?.env}={setupDep?.container_path}</code> {t('settings.extensions.needsSetupNote.after')}</span>
                                </p>
                            {/if}
                            {#if entry.capabilities?.network?.allowed_domains?.length}
                                <div class="ext-proxy-domains">
                                    <span class="ext-proxy-label">{t('settings.extensions.connectsTo')}</span>
                                    <div class="cap-detail-tags">
                                        {#each entry.capabilities.network.allowed_domains as domain}
                                            <button class="cap-detail-tag" onclick={() => { selectedExtension = { name: entry.name, manifest: { display_name: entry.display_name, description: entry.description, capabilities: getRegistryCapabilities(entry) }, type: entry.type } as Extension; showCapabilitiesModal = true; }}>{domain}</button>
                                        {/each}
                                    </div>
                                </div>
                            {/if}
                            {#if entry.type === 'agent'}
                                <p class="ext-note">
                                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="12" r="10"/><line x1="12" y1="16" x2="12" y2="12"/><line x1="12" y1="8" x2="12.01" y2="8"/></svg>
                                    {t('settings.extensions.agentNote.before')} {entry.display_name} {t('settings.extensions.agentNote.after')} <code>{t('settings.tab.providers')}</code>
                                </p>
                            {/if}
                        </div>
                    {/each}
                </div>
            {/if}
        </div>
    {/if}
</div>

<style>
    .extensions-tab {
        display: flex;
        flex-direction: column;
        gap: 1rem;
    }

    /* Sticky wrapper: view-tabs + search-row, flush with modal-header */
    .sticky-header {
        position: sticky;
        top: 0;
        z-index: 10;
        background: var(--color-surface-1);
    }

    /* In-card build progress (compact) */
    .ext-build {
        margin: 0 0 0.75rem;
    }

    .ext-build-bar {
        height: 6px;
        background: rgba(88, 166, 255, 0.15);
        border-radius: 3px;
        overflow: hidden;
    }

    .ext-build-fill {
        height: 100%;
        width: var(--pct, 0%);
        background: var(--color-accent);
        border-radius: 3px;
        transition: width 0.3s ease;
    }

    .ext-build-error .ext-build-fill {
        background: var(--color-status-red);
    }

    .ext-build-text {
        display: block;
        margin-top: 0.25rem;
        font-size: 0.75rem;
        color: var(--color-text-tertiary);
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
    }

    /* The withheld-diagnostics state, in the extension's own words (its manifest
       note). Wraps on purpose: it is a sentence to read, not a status to ellipsize. */
    .ext-env-note {
        margin: 0 0 0.75rem;
        padding: 0.4rem 0.6rem;
        border-left: 2px solid var(--color-status-yellow, #f0c000);
        background: rgba(240, 192, 0, 0.07);
        border-radius: 0 4px 4px 0;
    }

    .ext-env-note-text {
        display: block;
        font-size: 0.75rem;
        line-height: 1.45;
        color: var(--color-text-secondary);
    }

    /* The dependency-directory warning on a card: the Installed card shows a dot on the Edit icon and
       a badge that opens that dialog; a Browse card shows the badge as a plain label, because nothing
       is installed there yet. Same attention color as .unpublished-badge. */
    .needs-setup-badge {
        font-size: 0.68rem;
        font-weight: 700;
        letter-spacing: 0.04em;
        text-transform: uppercase;
        padding: 0.12rem 0.5rem;
        border-radius: 4px;
        background: var(--color-status-yellow);
        color: #111;
        border: 1px solid #111;
        white-space: nowrap;
        flex-shrink: 0;
    }

    .card-actions .btn-icon {
        position: relative;
    }

    .ext-needs-dot {
        position: absolute;
        top: 1px;
        right: 1px;
        width: 7px;
        height: 7px;
        border-radius: 50%;
        background: var(--color-status-yellow);
        pointer-events: none;
    }

    /* In the Edit dialog: the location the manifest declared, the field that names the host directory,
       and the grant as it stands. Quieter than a warning — this is a setting, not a fault. */
    .ext-dep-env,
    .ext-dep-path,
    .ext-dep-input {
        font-family: var(--font-mono, monospace);
        font-size: 0.75rem;
    }

    .ext-dep-state {
        display: flex;
        align-items: center;
        gap: 0.45rem;
        flex-wrap: wrap;
        min-width: 0;
        margin-top: 0.35rem;
    }

    .ext-dep-state-label {
        font-size: 0.72rem;
        text-transform: uppercase;
        letter-spacing: 0.04em;
        color: var(--color-text-tertiary);
        flex: none;
    }

    .ext-dep-path {
        min-width: 0;
        overflow: hidden;
        text-overflow: ellipsis;
        white-space: nowrap;
        color: var(--color-text);
    }

    .ext-dep-none {
        font-size: 0.75rem;
        color: var(--color-text-secondary);
        font-style: italic;
    }

    .ext-dep-pending {
        font-size: 0.72rem;
        color: var(--color-status-yellow);
    }

    .ext-dep-error {
        margin: 0.3rem 0 0;
        font-size: 0.75rem;
        line-height: 1.45;
        color: var(--color-status-red, #f85149);
    }

    .error-banner {
        position: sticky;
        top: 0;
        z-index: 11;
        background: var(--color-surface-1);
        border: 1px solid rgba(248, 81, 73, 0.4);
        border-left: 4px solid var(--color-status-red);
        border-radius: 6px;
        padding: 0.75rem 1rem;
        color: var(--color-status-red);
        cursor: pointer;
        display: flex;
        justify-content: space-between;
        align-items: center;
        box-shadow: 0 2px 8px rgba(0, 0, 0, 0.15);
    }

    .dismiss {
        background: none;
        border: none;
        color: inherit;
        cursor: pointer;
        font-size: 1rem;
        padding: 0 0.25rem;
    }

    /* View tabs */
    .view-tabs {
        display: flex;
        align-items: center;
        justify-content: space-between;
        gap: 0.5rem;
        border-bottom: 1px solid var(--color-border-subtle);
        padding: 0.5rem 0;
    }

    .view-tabs-left {
        display: flex;
        gap: 0.5rem;
    }

    .view-tabs-right {
        display: flex;
        gap: 0.5rem;
        flex-shrink: 0;
    }

    .view-tab {
        background: none;
        border: none;
        color: var(--color-text-tertiary);
        cursor: pointer;
        font-size: 0.875rem;
        padding: 0.5rem 1rem;
        border-radius: 6px;
        transition: all 0.15s;
    }

    .view-tab:hover {
        background: rgba(139, 148, 158, 0.1);
        color: var(--color-text-primary);
    }

    .view-tab.active {
        background: rgba(88, 166, 255, 0.15);
        color: var(--color-accent);
    }

    /* Search */
    .search-row {
        display: flex;
        gap: 0.5rem;
        padding: 0.25rem 0;
    }

    .search-input {
        flex: 1;
        background: var(--color-bg-input);
        border: 1px solid var(--color-border-subtle);
        border-radius: 6px;
        padding: 0.5rem 0.75rem;
        color: var(--color-text-primary);
        font-size: 0.875rem;
    }

    .search-input:focus {
        outline: none;
        border-color: var(--color-accent);
    }

    /* Updates section */
    .updates-section {
        background: rgba(88, 166, 255, 0.08);
        border: 1px solid rgba(88, 166, 255, 0.2);
        border-radius: 8px;
        padding: 1rem;
    }

    .updates-section h3 {
        margin: 0;
        font-size: 0.875rem;
        color: var(--color-accent);
    }

    .updates-header {
        display: flex;
        justify-content: space-between;
        align-items: center;
        margin-bottom: 0.5rem;
        gap: 1rem;
    }

    .updates-none .updates-no-result {
        margin: 0.25rem 0 0;
        font-size: 0.85rem;
        color: var(--color-text-secondary);
    }

    .update-item {
        display: flex;
        flex-wrap: wrap;
        justify-content: space-between;
        align-items: center;
        gap: 0.5rem 1rem;
        padding: 0.5rem 0;
    }

    .update-item:not(:last-child) {
        border-bottom: 1px solid rgba(88, 166, 255, 0.15);
    }

    .update-item.has-new-perms {
        padding: 0.75rem;
        margin: 0.5rem 0;
        background: rgba(255, 166, 0, 0.1);
        border: 1px solid rgba(255, 166, 0, 0.4);
        border-radius: 8px;
    }

    .update-info {
        display: flex;
        align-items: baseline;
        flex: 1 1 0;
        min-width: 0;
    }

    .update-name {
        font-weight: 600;
        color: var(--color-text-primary);
        min-width: 0;
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
    }

    .update-versions {
        font-size: 0.8rem;
        color: var(--color-text-tertiary);
        margin-left: 0.5rem;
        flex-shrink: 0;
    }

    .update-action {
        margin-left: auto;
        flex-shrink: 0;
    }

    .update-new-perms {
        flex-basis: 100%;
        margin: 0.25rem 0 0.5rem;
        padding: 0.6rem 0.75rem;
        background: rgba(255, 166, 0, 0.12);
        border: 1px solid rgba(255, 166, 0, 0.35);
        border-radius: 6px;
    }

    .update-new-perms-title {
        font-size: 0.8rem;
        font-weight: 700;
        color: var(--color-warning, #e0a030);
        margin-bottom: 0.4rem;
    }

    .update-new-perms-list {
        list-style: none;
        margin: 0;
        padding: 0;
        display: flex;
        flex-direction: column;
        gap: 0.4rem;
    }

    .update-new-perm {
        display: flex;
        flex-direction: column;
        gap: 0.05rem;
    }

    .update-new-perm-name {
        display: inline-flex;
        align-items: center;
        gap: 4px;
        font-size: 0.82rem;
        font-weight: 600;
        color: var(--color-text-primary);
    }

    .update-new-perm-desc {
        font-size: 0.78rem;
        color: var(--color-text-secondary);
        line-height: 1.4;
    }

    .update-changelog {
        flex-basis: 100%;
        margin: 0.25rem 0 0.5rem;
        padding: 0.6rem 0.75rem;
        background: rgba(88, 166, 255, 0.08);
        border: 1px solid rgba(88, 166, 255, 0.25);
        border-radius: 6px;
    }

    .update-changelog-title {
        font-size: 0.8rem;
        font-weight: 700;
        color: var(--color-text-secondary);
        margin-bottom: 0.4rem;
    }

    .update-changelog-list {
        list-style: none;
        margin: 0;
        padding: 0;
        display: flex;
        flex-direction: column;
        gap: 0.3rem;
    }

    .update-changelog-item {
        display: flex;
        align-items: baseline;
        gap: 0.5rem;
    }

    .update-changelog-desc {
        font-size: 0.82rem;
        color: var(--color-text-primary);
        line-height: 1.4;
    }

    .update-changelog-hash {
        font-size: 0.72rem;
        color: var(--color-text-tertiary);
        background: rgba(88, 166, 255, 0.12);
        padding: 0.05rem 0.3rem;
        border-radius: 4px;
        white-space: nowrap;
    }

    /* Extension list */
    .extension-list {
        display: flex;
        flex-direction: column;
        gap: 0.75rem;
        margin-top: 1rem;
    }

    .extension-card {
        background: var(--color-bg-card);
        border: 1px solid var(--color-border-subtle);
        border-radius: 8px;
        padding: 1rem;
        transition: border-color 0.15s;
    }

    .extension-card:hover {
        border-color: var(--color-border-hover);
    }

    .extension-card.disabled {
        opacity: 0.6;
    }

    .registry-card {
        border-style: dashed;
    }

    .unpublished-card {
        position: relative;
        border-color: transparent;
        background: color-mix(in srgb, var(--color-status-yellow) 6%, var(--color-bg-card));
    }

    .unpublished-card::before {
        content: '';
        position: absolute;
        inset: 0;
        border-radius: inherit;
        padding: 2px; /* stripe border thickness */
        background: repeating-linear-gradient(
            45deg,
            var(--color-status-yellow) 0 8px,
            #111 8px 16px
        );
        -webkit-mask:
            linear-gradient(#fff 0 0) content-box,
            linear-gradient(#fff 0 0);
        -webkit-mask-composite: xor;
        mask:
            linear-gradient(#fff 0 0) content-box,
            linear-gradient(#fff 0 0);
        mask-composite: exclude;
        pointer-events: none;
    }

    .unpublished-card:hover {
        filter: brightness(1.02);
    }

    .unpublished-badge {
        font-size: 0.68rem;
        font-weight: 700;
        letter-spacing: 0.04em;
        text-transform: uppercase;
        padding: 0.12rem 0.5rem;
        border-radius: 4px;
        background: var(--color-status-yellow);
        color: #111;
        border: 1px solid #111;
        white-space: nowrap;
        flex-shrink: 0;
    }

    .featured-badge {
        font-size: 0.68rem;
        font-weight: 700;
        letter-spacing: 0.04em;
        text-transform: uppercase;
        padding: 0.12rem 0.5rem;
        border-radius: 4px;
        background: var(--color-accent);
        color: #fff;
        border: 1px solid var(--color-accent);
        white-space: nowrap;
        flex-shrink: 0;
    }

    /* In the Installed view the badge is a button that reveals the path. */
    .extension-card .unpublished-badge[type='button'] {
        cursor: pointer;
        text-decoration: none;
    }
    .extension-card .unpublished-badge[type='button']:hover {
        filter: brightness(1.08);
        box-shadow: 0 0 0 2px color-mix(in srgb, var(--color-status-yellow) 40%, transparent);
    }
    .extension-card .unpublished-badge[type='button'][aria-expanded='true'] {
        box-shadow: 0 0 0 2px var(--color-status-yellow);
    }

    .local-path {
        display: flex;
        align-items: baseline;
        gap: 0.5rem;
        margin: 0 0 0.5rem;
        padding: 0.35rem 0.6rem;
        border-radius: 4px;
        background: color-mix(in srgb, var(--color-status-yellow) 10%, var(--color-bg-card));
        border: 1px solid color-mix(in srgb, var(--color-status-yellow) 35%, transparent);
        font-size: 0.75rem;
        overflow: hidden;
    }
    .local-path-label {
        font-weight: 700;
        text-transform: uppercase;
        letter-spacing: 0.04em;
        font-size: 0.65rem;
        color: color-mix(in srgb, var(--color-text) 70%, transparent);
        flex-shrink: 0;
    }
    .local-path-code {
        font-family: var(--font-mono, monospace);
        font-size: 0.75rem;
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
        color: var(--color-text);
    }
    .local-path-unknown {
        color: color-mix(in srgb, var(--color-text) 55%, transparent);
        font-style: italic;
    }

    .card-header {
        display: flex;
        align-items: flex-start;
        gap: 0.75rem;
        margin-bottom: 0.5rem;
    }

    .ext-icon {
        width: 2.5rem;
        height: 2.5rem;
        border-radius: 6px;
        display: flex;
        align-items: center;
        justify-content: center;
        font-size: 0.75rem;
        font-weight: 700;
        flex-shrink: 0;
    }

    .ext-lsp {
        background: rgba(63, 185, 80, 0.15);
        color: var(--color-status-green);
    }

    .ext-ui {
        background: rgba(76, 154, 255, 0.15);
        color: var(--color-status-blue);
    }

    .ext-css {
        background: rgba(227, 179, 65, 0.15);
        color: var(--color-status-yellow);
    }

    .ext-agent {
        background: rgba(163, 113, 247, 0.15);
        color: var(--color-status-purple);
    }

    .ext-llm {
        background: var(--color-text-inverse);
        color: var(--color-text-secondary);
    }

        /* A translation pack: its own accent, so an installed pack is not mistaken
           for a UI plugin. */
        .ext-i18n {
            background: rgba(210, 153, 34, 0.15);
            color: var(--color-status-orange);    }

    .ext-info {
        flex: 1;
        min-width: 0;
        display: flex;
        flex-direction: column;
        gap: 0.15rem;
    }

    .ext-name-row {
        display: flex;
        align-items: center;
        flex-wrap: wrap;
        gap: 0.35rem;
        min-width: 0;
    }

    .ext-name {
        font-weight: 600;
        color: var(--color-text-primary);
        font-size: 0.95rem;
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
    }

    .ext-meta {
        font-size: 0.8rem;
        color: var(--color-text-tertiary);
    }

    .ext-link {
        color: var(--color-accent);
        text-decoration: none;
    }

    .ext-link:hover {
        text-decoration: underline;
    }

    .card-actions {
        display: flex;
        align-items: center;
        gap: 0.5rem;
        flex-shrink: 0;
    }

    :global(.card-toggle) {
        margin-left: auto;
    }

    .ext-action {
        flex-shrink: 0;
        display: flex;
        align-items: flex-start;
    }

    .ext-description {
        margin: 0 0 0.75rem;
        font-size: 0.85rem;
        color: var(--color-text-tertiary);
        line-height: 1.4;
    }

    .ext-note {
        margin: 0 0 0.5rem;
        font-size: 0.8rem;
        color: var(--color-text-tertiary);
        line-height: 1.4;
        display: flex;
        align-items: flex-start;
        gap: 0.35rem;
    }

    .ext-proxy-domains {
        display: flex;
        flex-wrap: wrap;
        align-items: center;
        gap: 0.3rem 0.5rem;
        margin: 0 0 0.75rem;
        font-size: 0.8rem;
        color: var(--color-text-tertiary);
    }

    .ext-proxy-label {
        font-size: 0.72rem;
        font-weight: 600;
        text-transform: uppercase;
        letter-spacing: 0.03em;
    }

    .ext-note svg {
        flex-shrink: 0;
        margin-top: 2px;
    }

    .ext-note code {
        background: var(--color-bg-input);
        padding: 0.1em 0.35em;
        border-radius: 3px;
        font-size: 0.78rem;
    }

    /* Capability badges */
    .capability-badges {
        display: flex;
        flex-wrap: wrap;
        gap: 0.25rem;
        flex-shrink: 0;
    }

    .cap-badge {
        font-size: 0.7rem;
        font-weight: 600;
        padding: 0.15rem 0.5rem;
        border-radius: 4px;
        border: 1px solid transparent;
        cursor: pointer;
        transition: all 0.15s;
        text-transform: uppercase;
        letter-spacing: 0.03em;
    }

    .cap-badge:hover {
        filter: brightness(1.3);
        border-color: currentColor;
    }

    .cap-lsp {
        background: rgba(63, 185, 80, 0.15);
        color: var(--color-status-green);
        border-color: rgba(63, 185, 80, 0.3);
    }

    .cap-ui {
        background: rgba(76, 154, 255, 0.15);
        color: var(--color-status-blue);
        border-color: rgba(76, 154, 255, 0.3);
    }

    .cap-css {
        background: rgba(227, 179, 65, 0.15);
        color: var(--color-status-yellow);
        border-color: rgba(227, 179, 65, 0.3);
    }

    .cap-agent {
        background: rgba(163, 113, 247, 0.15);
        color: var(--color-status-purple);
        border-color: rgba(163, 113, 247, 0.3);
    }

    .cap-network {
        background: rgba(236, 72, 153, 0.15);
        color: #ec4899;
        border-color: rgba(236, 72, 153, 0.3);
    }

    .cap-llm {
        background: var(--color-text-inverse);
        color: var(--color-text-secondary);
        border-color: rgba(222, 222, 222, 0.3);
    }

    .cap-i18n {
            background: rgba(210, 153, 34, 0.15);
            color: var(--color-status-orange);
            border-color: rgba(210, 153, 34, 0.3);    }

    /* Capabilities modal */
    .caps-modal-body {
        display: flex;
        flex-direction: column;
        gap: 1.5rem;
        padding: 0.5rem 0;
    }

    .caps-ext-header {
        display: flex;
        align-items: center;
        gap: 0.75rem;
    }

    .caps-ext-icon {
        width: 2.5rem;
        height: 2.5rem;
        border-radius: 6px;
        display: flex;
        align-items: center;
        justify-content: center;
        font-size: 0.75rem;
        font-weight: 700;
        flex-shrink: 0;
    }

    .caps-ext-info {
        display: flex;
        flex-direction: column;
        gap: 0.15rem;
    }

    .caps-ext-name {
        margin: 0;
        font-weight: 600;
        color: var(--color-text-primary);
        font-size: 1rem;
    }

    .caps-ext-desc {
        margin: 0;
        font-size: 0.8rem;
        color: var(--color-text-tertiary);
    }

    .caps-list {
        display: flex;
        flex-direction: column;
        gap: 1rem;
    }

    .cap-detail {
        display: flex;
        flex-direction: column;
        gap: 0.35rem;
        padding: 0.75rem;
        background: var(--color-bg-card);
        border: 1px solid var(--color-border-subtle);
        border-radius: 8px;
    }

    .cap-detail-header {
        display: flex;
        align-items: center;
        gap: 0.5rem;
    }

    .cap-detail-badge {
        font-size: 0.75rem;
        font-weight: 600;
        padding: 0.2rem 0.6rem;
        border-radius: 4px;
        text-transform: uppercase;
        letter-spacing: 0.03em;
    }

    .cap-detail-desc {
        margin: 0;
        font-size: 0.85rem;
        color: var(--color-text-secondary);
        line-height: 1.5;
    }

    .cap-detail-extra {
        display: flex;
        flex-wrap: wrap;
        align-items: flex-start;
        gap: 0.3rem 0.5rem;
        margin-top: 0.5rem;
        padding-top: 0.5rem;
        border-top: 1px solid var(--color-border-subtle);
    }

    .cap-detail-label {
        font-size: 0.75rem;
        font-weight: 600;
        color: var(--color-text-tertiary);
        text-transform: uppercase;
        letter-spacing: 0.03em;
    }

    .cap-detail-value {
        font-size: 0.82rem;
        color: var(--color-text-secondary);
    }

    .cap-detail-tags {
        display: flex;
        flex-wrap: wrap;
        gap: 0.3rem;
    }

    .cap-detail-tag {
        font-size: 0.75rem;
        padding: 0.15rem 0.5rem;
        border-radius: 4px;
        background: var(--color-bg-input);
        border: 1px solid var(--color-border-subtle);
        color: var(--color-text-secondary);
        white-space: nowrap;
    }

    button.cap-detail-tag {
        cursor: pointer;
        transition: all 0.15s;
        font: inherit;
        text-align: inherit;
    }

    button.cap-detail-tag:hover {
        background: var(--color-surface-2);
        border-color: var(--color-border-hover);
        color: var(--color-text-primary);
    }

    .ext-proxy-domains .cap-detail-tag {
        color: var(--color-status-red);
        border-color: var(--color-status-red);
        background: color-mix(in srgb, var(--color-status-red) 10%, var(--color-bg-input));
    }

    .cap-detail-extra-block {
        flex-direction: column;
        align-items: stretch;
    }

    .cap-permission-list {
        display: flex;
        flex-direction: column;
        gap: 0.4rem;
        width: 100%;
    }

    .cap-permission {
        display: flex;
        flex-direction: column;
        gap: 0.1rem;
        padding: 0.4rem 0.55rem;
        background: var(--color-bg-input);
        border: 1px solid var(--color-border-subtle);
        border-radius: 6px;
    }

    .cap-permission-name {
        display: inline-flex;
        align-items: center;
        gap: 4px;
        font-size: 0.8rem;
        font-weight: 600;
        color: var(--color-text-primary);
    }

    .perm-danger-icon {
        flex: none;
        color: var(--color-warning, #e0a030);
    }

    .cap-permission-desc {
        font-size: 0.78rem;
        color: var(--color-text-secondary);
        line-height: 1.4;
    }

    .install-confirm-intro {
        margin: 0 0 0.6rem;
        font-size: 0.85rem;
        color: var(--color-text-secondary);
    }

    /* Radio group (matches SandboxConfigModal) */
    .radio-group {
        display: flex;
        gap: 0;
        border: 1px solid var(--color-border-subtle);
        border-radius: var(--radius-sm, 4px);
        overflow: hidden;
        flex-shrink: 0;
    }

    .radio-label {
        display: flex;
        align-items: center;
        gap: 4px;
        padding: 3px 10px;
        font-size: 0.75rem;
        color: var(--color-text-secondary, var(--color-text-tertiary));
        cursor: pointer;
        transition: background 0.15s, color 0.15s;
        user-select: none;
    }

    .radio-label:first-child {
        border-right: 1px solid var(--color-border-subtle);
    }

    .radio-label.active {
        background: var(--color-accent-dark, rgba(88, 166, 255, 0.25));
        color: white;
    }

    .radio-label:hover:not(.active) {
        background: var(--color-surface-3, rgba(139, 148, 158, 0.1));
    }

    .radio-label input[type="radio"] {
        display: none;
    }

    .radio-text {
        white-space: nowrap;
    }

    .radio-group-small {
        border-radius: var(--radius-sm, 4px);
    }

    .radio-group-small .radio-label {
        padding: 2px 7px;
        font-size: 0.8125rem;
    }


    .btn {
        padding: 0.4rem 0.75rem;
        border-radius: 6px;
        font-size: 0.8rem;
        cursor: pointer;
        border: 1px solid transparent;
        transition: all 0.15s;
    }

    .btn:disabled {
        opacity: 0.5;
        cursor: not-allowed;
    }

    .btn-primary {
        background: var(--color-btn-primary-bg);
        color: var(--color-btn-text);
    }

    .btn-primary:hover:not(:disabled) {
        background: var(--color-btn-primary-hover);
    }

    .btn-secondary {
        background: var(--color-btn-secondary-bg);
        border-color: var(--color-border-subtle);
        color: var(--color-text-primary);
    }

    .btn-secondary:hover:not(:disabled) {
        background: var(--color-border-subtle);
    }

    .btn-icon {
        background: none;
        border: none;
        color: var(--color-text-tertiary);
        padding: 0.25rem;
        font-size: 1rem;
    }

    .btn-icon:hover {
        color: var(--color-text-primary);
    }

    .btn-danger:hover {
        color: var(--color-status-red);
    }

    .btn-small {
        padding: 0.25rem 0.5rem;
        font-size: 0.75rem;
    }

    .installed-badge {
        font-size: 0.8rem;
        color: var(--color-status-green);
        padding: 0.4rem 0.75rem;
        background: rgba(63, 185, 80, 0.1);
        border-radius: 6px;
    }

    /* Spinner */
    .spinner {
        display: inline-block;
        width: 12px;
        height: 12px;
        border: 2px solid rgba(255, 255, 255, 0.3);
        border-top-color: var(--color-btn-text);
        border-radius: 50%;
        animation: spin 0.6s linear infinite;
        vertical-align: middle;
        margin-right: 0.35rem;
    }

    @keyframes spin {
        to { transform: rotate(360deg); }
    }

    .field-label {
        display: block;
        font-size: 0.8rem;
        color: var(--color-text-tertiary);
        margin-bottom: 0.25rem;
    }

    /* Empty state */
    .empty-state {
        text-align: center;
        padding: 3rem 1rem;
        color: var(--color-text-tertiary);
    }

    .empty-state p {
        margin: 0.5rem 0;
    }

    .empty-state .help {
        font-size: 0.85rem;
    }

    .empty-state .btn {
        margin-top: 1rem;
    }

    .loading {
        text-align: center;
        padding: 2rem;
        color: var(--color-text-tertiary);
    }

    .config-fields {
        display: flex;
        flex-direction: column;
        gap: 1rem;
        margin-bottom: 1.5rem;
    }

    .config-fields h4 {
        margin: 0;
        font-size: 0.85rem;
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
        border-radius: 6px;
        padding: 0.5rem 0.75rem;
        color: var(--color-text-primary);
        font-size: 0.875rem;
    }

    .config-input:focus {
        outline: none;
        border-color: var(--color-accent);
    }

    .no-schema {
        color: var(--color-text-tertiary);
        font-size: 0.85rem;
        margin-bottom: 1.5rem;
    }

    .field-desc {
        color: var(--color-text-tertiary);
        font-size: 0.75rem;
        margin: 0;
    }

    /* Mount/Env editors */
    .mount-editor,
    .env-editor {
        display: flex;
        flex-direction: column;
        gap: 0.35rem;
    }

    .mount-list,
    .env-list {
        display: flex;
        flex-direction: column;
        gap: 0.35rem;
    }

    .mount-row,
    .env-row {
        display: flex;
        align-items: center;
        gap: 0.35rem;
    }

    .mount-input {
        background: var(--color-bg-input);
        border: 1px solid var(--color-border-subtle);
        border-radius: 6px;
        padding: 0.4rem 0.5rem;
        color: var(--color-text-primary);
        font-size: 0.8125rem;
        flex: 1;
        min-width: 0;
    }

    .mount-input:focus {
        outline: none;
        border-color: var(--color-accent);
    }

    .mount-arrow {
        flex-shrink: 0;
        color: var(--color-text-tertiary);
    }

    .env-input {
        background: var(--color-bg-input);
        border: 1px solid var(--color-border-subtle);
        border-radius: 6px;
        padding: 0.4rem 0.5rem;
        color: var(--color-text-primary);
        font-size: 0.8125rem;
        flex: 1;
        min-width: 0;
    }

    .env-input:focus {
        outline: none;
        border-color: var(--color-accent);
    }

    .env-input-key {
        max-width: 120px;
        flex: none;
    }

    .env-equals {
        color: var(--color-text-tertiary);
        font-size: 0.8125rem;
        flex-shrink: 0;
    }

    .btn-remove {
        display: flex;
        align-items: center;
        justify-content: center;
        width: 22px;
        height: 22px;
        padding: 0;
        border: none;
        background: none;
        color: var(--color-text-tertiary);
        cursor: pointer;
        border-radius: 4px;
        flex-shrink: 0;
    }

    .btn-remove:hover {
        background: var(--color-surface-3);
        color: var(--color-status-red);
    }

    .btn-add {
        display: flex;
        align-items: center;
        justify-content: center;
        width: 26px;
        height: 26px;
        padding: 0;
        border: 1px dashed var(--color-border-subtle);
        background: none;
        color: var(--color-text-secondary);
        cursor: pointer;
        border-radius: 6px;
        align-self: flex-start;
    }

    .btn-add:hover {
        border-color: var(--color-accent);
        color: var(--color-accent);
    }

</style>
