<script lang="ts">
    import type { Extension, RegistryEntry, ExtensionUpdate, ExtensionType } from '$lib/types';
    import {
        checkUpdates,
        deleteExtension,
        getHomeDirectory,
        getRegistryIndex,
        installExtension,
        installLocalExtension,
        listExtensions,
        listLocalExtensions,
        setAgentMount,
        setExtensionEnabled,
        updateExtensionImage,
    } from '$lib/api';
    import { t } from '$lib/i18n';
    import { dialogStore } from '$lib/stores';
    import ExtensionsHeader from './ExtensionsHeader.svelte';
    import ExtensionBrowseCard from './ExtensionBrowseCard.svelte';
    import ExtensionCapabilitiesModal from './ExtensionCapabilitiesModal.svelte';
    import ExtensionCard from './ExtensionCard.svelte';
    import ExtensionEditModal from './ExtensionEditModal.svelte';
    import ExtensionInstallConfirmModal from './ExtensionInstallConfirmModal.svelte';
    import ExtensionUpdatesPanel from './ExtensionUpdatesPanel.svelte';
    import EmptyState from '../EmptyState.svelte';
    import type { OperationProgress } from './ExtensionBuildProgress.svelte';
    import { ExtensionDraft } from './extensions-draft.svelte';
    import { agentMountDeclaration, needsAgentMountGrant, needsDependencyGrant, requiresInstallReview } from '$lib/utils/extension-permissions';
    import { registryCapabilities } from '$lib/utils/extension-manifest';
    import { countByCategory, filterByCategory, visibleCategories } from '$lib/utils/extension-categories';
    import { identityKey } from '$lib/utils/extension-identity';
    import { orderBrowseEntries } from '$lib/composables/use-browse-order';

    /**
     * Settings → Extensions. This file owns the two lists and everything that changes them: the install
     * and update streams, the toggles, the deletes, and the consent dialog that can stop an install
     * before it starts. What a row looks like, what a draft holds and what a dialog sends live in the
     * components it renders.
     *
     * "Which extension is busy" is decided here and nowhere else, because an install that starts twice
     * leaves the name claimed in the server's in-flight set with no row and no way for the screen to say
     * why. Progress comes from the operation's stream alone — there is no build-progress route to poll.
     */
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
    /** Names of rows `Update all` left alone because updating them needs a decision. */
    let updatesAwaitingReview = $state<string[]>([]);
    let error = $state('');
    let searchQuery = $state('');
    /** Categories (extension types) the lists are narrowed to; an empty set narrows nothing. */
    let selectedCategories = $state<Set<ExtensionType>>(new Set());

    // The one owner of "which extension is busy", and of the progress each busy row shows.
    let installing = $state<Set<string>>(new Set());
    let updating = $state<Set<string>>(new Set());
    let buildProgress = $state<Record<string, OperationProgress>>({});

    // Capabilities modal
    let showCapabilitiesModal = $state(false);
    let selectedExtension: Extension | null = $state(null);

    // Extension id whose local path is revealed (clicked its "Local" badge).
    let revealedLocalPath = $state<string | null>(null);

    // Set when the user clicks Install on an extension that has to be reviewed first, because it asks for
    // network access, for permissions that escape the sandbox, or for a host folder. Closing this dialog
    // installs nothing.
    let pendingInstall = $state<{ entry: RegistryEntry; isLocal: boolean } | null>(null);

    /** The dialog only offers the choice when this is not empty. */
    let pendingAgentMounts = $derived(pendingInstall ? agentMountDeclaration(pendingInstall.entry.capabilities) : []);

    /** The Edit dialog's unsaved values, and the probe answers that belong to it. */
    const draft = new ExtensionDraft();

    // The card to send the user to after an install. It is focused rather than opened in a dialog, because
    // several installs can be running at once.
    let setupFocusId = $state('');

    // Load extensions on mount
    async function loadExtensions() {
        loading = true;
        error = '';
        // The highlight refers to the list that is loaded now, so loading again clears it.
        setupFocusId = '';
        try {
            extensions = await listExtensions();
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

    // Open Browse and refresh both of its lists. The registry is always refetched: the server answers
    // it `no-cache`, so an index that has not moved comes back as a 304 rather than as a second fetch
    // of the same content.
    function openBrowse() {
        activeView = 'browse';
        loadLocalExtensions();
        loadRegistry();
    }

    // Remove a per-extension progress entry (reactive reassignment).
    function clearBuildProgress(name: string) {
        const next = { ...buildProgress };
        delete next[name];
        buildProgress = next;
    }

    // Record the latest progress event for an extension.
    function setBuildProgress(name: string, progress: number, message: string, err?: string) {
        buildProgress = { ...buildProgress, [name]: { progress, message, error: err } };
    }

    // Kick off an install from Browse. Extensions that declare the network capability or sandbox-escaping
    // permissions first go through the install confirm dialog, so the user reviews what they are granting
    // before the install starts.
    function handleInstall(entry: RegistryEntry, isLocal: boolean) {
        if (requiresInstallReview(entry)) {
            pendingInstall = { entry, isLocal };
            return;
        }
        void runInstall(entry, isLocal, false);
    }

    /** `true` means the user agreed to mount the suggested folders, which happens once the extension has
     *  been saved. */
    function confirmPendingInstall(mount: boolean) {
        const pending = pendingInstall;
        pendingInstall = null;
        if (pending) void runInstall(pending.entry, pending.isLocal, mount);
    }

    // Install an extension, streaming progress into the card as it happens.
    // Local (unpublished) extensions install from the filesystem, bypassing the registry.
    async function runInstall(entry: RegistryEntry, isLocal: boolean, mount: boolean) {
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
            // Only after the install stream ends: the extension has to be saved before a folder can be
            // approved for it.
            if (mount) await grantProposedMount(entry);
            focusCardNeedingSetup();
        } catch (e: any) {
            error = e.message || t('settings.extensions.installFailed');
        } finally {
            const next = new Set(installing);
            next.delete(name);
            installing = next;
        }
    }

    async function grantProposedMount(entry: RegistryEntry) {
        const proposal = agentMountDeclaration(entry.capabilities);
        if (!proposal.length) return;
        const installed = extensions.find((e) => identityKey(e.author, e.name) === identityKey(entry.author, entry.name));
        if (!installed) return;
        try {
            await setAgentMount(installed.id, proposal.map((m) => ({ host_path: m.host_path, container_path: m.container_path, mode: m.mode })));
            await loadExtensions();
        } catch (e: any) {
            error = t('settings.extensions.agentGrantFailed', { reason: e?.message || String(e) });
        }
    }

    function focusCardNeedingSetup() {
        const waiting = extensions.find((e) => needsAgentMountGrant(e) || needsDependencyGrant(e));
        setupFocusId = waiting?.id ?? '';
    }

    $effect(() => {
        const id = setupFocusId;
        if (!id) return;
        // Focus the card's "Needs setup" badge: it explains the state and can be reached by keyboard.
        const badge = document.getElementById(`ext-card-${id}`);
        if (badge) {
            badge.focus();
            badge.scrollIntoView({ block: 'nearest' });
        }
    });

    // Clears the approval straight from the card, without opening the edit dialog.
    async function revokeAgentMount(ext: Extension) {
        error = '';
        try {
            await setAgentMount(ext.id, []);
            await loadExtensions();
        } catch (e: any) {
            error = e.message || t('settings.extensions.agentGrantFailed', { reason: e?.message || String(e) });
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
    async function handleToggleEnabled(ext: Extension) {
        error = '';
        try {
            await setExtensionEnabled(ext.id, !ext.enabled);
            await loadExtensions();
        } catch (e: any) {
            error = e.message || t('settings.extensions.updateFailed');
        }
    }

    $effect(() => {
        // Fetch the home directory once, and only if some installed extension declares a dependency
        // cache. The suggestion is editable text, never a value applied behind the user's back.
        const declared = extensions.some((e) => e.sandbox_config?.dependency_cache);
        if (!declared || draft.homeDirectory || !draft.wantsHomeDirectory()) return;
        getHomeDirectory()
            .then((h) => { draft.setHomeDirectory(h); })
            .catch(() => { /* a suggestion is a convenience; typing the path is the fallback */ });
    });

    // Check for updates
    async function handleCheckUpdates() {
        activeView = 'installed';
        loadingUpdates = true;
        error = '';
        try {
            updates = await checkUpdates();
            updatesAwaitingReview = [];
            hasCheckedUpdates = true;
        } catch (e: any) {
            error = e.message || t('settings.extensions.checkUpdatesFailed');
        } finally {
            loadingUpdates = false;
        }
    }

    // Perform the real file/image replacement for one extension by streaming the
    // registry update operation. Returns true on success, false on failure.
    async function applyExtensionUpdate(id: string, name: string, acknowledgeNewDomains = false): Promise<boolean> {
        let ok = true;
        updating = new Set(updating).add(name);
        try {
            for await (const event of updateExtensionImage(id, acknowledgeNewDomains)) {
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
    // The user's answer goes to the server with the request, which refuses the update without it.
    async function handleUpdate(update: ExtensionUpdate) {
        error = '';
        let acknowledged = false;
        if (update.domains_added) {
            acknowledged = await dialogStore.showConfirm({
                title: t('settings.extensions.updateDomainsTitle'),
                message: t('settings.extensions.updateDomainsMessage', { name: update.name }),
                confirmText: t('settings.extensions.update'),
            });
            if (!acknowledged) return;
        }
        const ok = await applyExtensionUpdate(update.id, update.name, acknowledged);
        await loadExtensions();
        if (ok) {
            updates = updates.filter(u => u.id !== update.id);
            updatesAwaitingReview = updatesAwaitingReview.filter(n => n !== update.name);
        }
    }

    // Update all available extensions, sequentially, performing real replacements.
    // A row needing a decision is skipped, and stays listed for the user to decide.
    async function handleUpdateAll() {
        updatingAll = true;
        error = '';
        const awaiting = [...updates].filter(u => u.domains_added).map(u => u.name);
        try {
            for (const update of [...updates]) {
                if (update.domains_added) continue;
                const ok = await applyExtensionUpdate(update.id, update.name);
                if (ok) {
                    updates = updates.filter(u => u.id !== update.id);
                }
                // On failure the error is already set; continue with the rest.
            }
            updatesAwaitingReview = awaiting;
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

    // The detail dialog reads one shape, so a Browse entry is shown through the same record the
    // Installed cards use.
    function showCapabilities(ext: Extension) {
        selectedExtension = ext;
        showCapabilitiesModal = true;
    }

    function showRegistryCapabilities(entry: RegistryEntry) {
        selectedExtension = {
            name: entry.name,
            manifest: { display_name: entry.display_name, description: entry.description, capabilities: registryCapabilities(entry) },
            type: entry.type,
        } as Extension;
        showCapabilitiesModal = true;
    }

    function closeCapabilities() {
        showCapabilitiesModal = false;
        selectedExtension = null;
    }

    function openEdit(ext: Extension) {
        draft.open(ext);
    }

    function toggleCategory(category: ExtensionType) {
        const next = new Set(selectedCategories);
        if (next.has(category)) {
            next.delete(category);
        } else {
            next.add(category);
        }
        selectedCategories = next;
    }

    // Extensions matching the text query, with local (unpublished) extensions first
    let installedMatches = $derived.by(() => {
        const query = searchQuery.toLowerCase();
        const list = searchQuery
            ? extensions.filter(ext =>
                ext.name.toLowerCase().includes(query) ||
                ext.manifest.display_name.toLowerCase().includes(query) ||
                ext.type.includes(query)
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
    // registry entries, then the not-yet-installed translation pack for the
    // browser's language, then the remaining registry entries. See
    // orderBrowseEntries in $lib/composables/use-browse-order.
    let browseMatches = $derived.by(() =>
        orderBrowseEntries(
            unpublished,
            registryEntries,
            entry => installedKeys.has(identityKey(entry.author, entry.name)),
            searchQuery,
            typeof navigator === 'undefined' ? '' : navigator.language ?? '',
        )
    );

    // The category badges narrow each view in place, so the local-first sort and the two pins keep
    // deciding position exactly as their own resolver describes it.
    let filteredExtensions = $derived(filterByCategory(installedMatches, selectedCategories));
    let browseList = $derived(filterByCategory(browseMatches, selectedCategories));

    // The badge row follows the view that is on screen.
    let categoryCounts = $derived(countByCategory(activeView === 'browse' ? browseMatches : installedMatches));
    let categories = $derived(visibleCategories(categoryCounts));

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

    <ExtensionsHeader
        {activeView}
        installedCount={extensions.length}
        {loadingUpdates}
        {searchQuery}
        {categories}
        {categoryCounts}
        {selectedCategories}
        onloadupdates={handleCheckUpdates}
        onview={(view) => { if (view === 'browse') openBrowse(); else activeView = 'installed'; }}
        onsearch={(query) => searchQuery = query}
        ontogglecategory={toggleCategory}
        onclearcategories={() => selectedCategories = new Set()}
    />

    <!-- Installed View -->
    {#if activeView === 'installed'}
        <div class="installed-view">
            <!-- Updates Available -->
            <ExtensionUpdatesPanel
                {updates}
                hasChecked={hasCheckedUpdates}
                {updatingAll}
                awaitingReview={updatesAwaitingReview}
                onupdate={handleUpdate}
                onupdateall={handleUpdateAll}
            />

            <!-- Extension List -->
            {#if loading}
                <div class="loading">{t('settings.extensions.loadingExtensions')}</div>
            {:else if filteredExtensions.length === 0}
                <EmptyState title={t('settings.extensions.noneInstalled')} hint={t('settings.extensions.noneInstalledHelp')}>
                    {#snippet action()}
                        <button class="btn btn-primary" onclick={openBrowse}>
                            {t('settings.extensions.browse')}
                        </button>
                    {/snippet}
                </EmptyState>
            {:else}
                <div class="extension-list">
                    {#each filteredExtensions as ext}
                        <ExtensionCard
                            {ext}
                            progress={buildProgress[ext.name]}
                            updating={updating.has(ext.name)}
                            setupFocus={setupFocusId === ext.id}
                            revealed={revealedLocalPath === ext.id}
                            onreveal={() => { revealedLocalPath = revealedLocalPath === ext.id ? null : ext.id; }}
                            oncaps={showCapabilities}
                            onedit={openEdit}
                            ontoggle={handleToggleEnabled}
                            ondelete={handleDelete}
                            onupdateimage={handleUpdateImage}
                            onrevoke={revokeAgentMount}
                        />
                    {/each}
                </div>
            {/if}
        </div>
    {/if}

    <!-- Edit Extension Modal -->
    {#if draft.extension}
        <ExtensionEditModal
            {draft}
            onclose={() => draft.close()}
            onsaved={loadExtensions}
            onerror={(message) => { error = message; }}
        />
    {/if}

    <!-- Capabilities Modal -->
    {#if showCapabilitiesModal && selectedExtension}
        <ExtensionCapabilitiesModal extension={selectedExtension} onclose={closeCapabilities} />
    {/if}

    <!-- Shown before installing an extension that asks for network access, for permissions that escape
         the sandbox, or for a host folder. Closing it installs nothing. -->
    {#if pendingInstall}
        <ExtensionInstallConfirmModal
            entry={pendingInstall.entry}
            agentMounts={pendingAgentMounts}
            onconfirm={confirmPendingInstall}
            onclose={() => { pendingInstall = null; }}
        />
    {/if}

    <!-- Browse View -->
    {#if activeView === 'browse'}
        <div class="browse-view">

            <!-- Registry List -->
            {#if loadingRegistry}
                <div class="loading">{t('settings.extensions.loadingRegistry')}</div>
            {:else if browseList.length === 0}
                <EmptyState title={t('settings.extensions.registryEmpty')} hint={t('settings.extensions.registryEmptyHelp')} />
            {:else}
                <div class="extension-list">
                    {#each browseList as entry}
                        {@const identity = identityKey(entry.author, entry.name)}
                        {@const unpublishedEntry = unpublishedKeys.has(identity)}
                        <ExtensionBrowseCard
                            {entry}
                            progress={buildProgress[entry.name]}
                            installing={installing.has(entry.name)}
                            installed={installedKeys.has(identity)}
                            unpublished={unpublishedEntry}
                            revealed={revealedLocalPath === identity}
                            onreveal={() => { revealedLocalPath = revealedLocalPath === identity ? null : identity; }}
                            oncaps={showRegistryCapabilities}
                            oninstall={handleInstall}
                        />
                    {/each}
                </div>
            {/if}
        </div>
    {/if}
</div>

<style>
    @import '$lib/styles/extensions.css';

    .extensions-tab {
        display: flex;
        flex-direction: column;
        gap: 1rem;
    }

    /* Clears ExtensionsHeader's sticky bar, which shares this top edge and sits earlier in the
       DOM; the dropdown rank is the next one up. */
    .error-banner {
        position: sticky;
        top: 0;
        z-index: var(--z-dropdown);
        background: var(--color-surface-1);
        border: 1px solid rgb(from var(--color-status-red) r g b / 0.4);
        border-left: 4px solid var(--color-status-red);
        border-radius: var(--radius-md);
        padding: 0.75rem 1rem;
        color: var(--color-status-red);
        cursor: pointer;
        display: flex;
        justify-content: space-between;
        align-items: center;
        box-shadow: 0 2px 8px rgb(from var(--color-shadow) r g b / 0.15);
    }

    .dismiss {
        background: none;
        border: none;
        color: inherit;
        cursor: pointer;
        font-size: var(--text-lg);
        padding: 0 0.25rem;
    }

    /* Extension list */
    .extension-list {
        display: flex;
        flex-direction: column;
        gap: 0.75rem;
        margin-top: 1rem;
    }

    .btn {
        padding: 0.4rem 0.75rem;
        border-radius: var(--radius-md);
        font-size: var(--text-sm);
        cursor: pointer;
        border: 1px solid transparent;
        transition: all 0.15s;
    }

    .btn-primary {
        background: var(--color-btn-primary-bg);
        color: var(--color-btn-text);
    }

    .btn-primary:hover:not(:disabled) {
        background: var(--color-btn-primary-hover);
    }

    .loading {
        text-align: center;
        padding: 2rem;
        color: var(--color-text-tertiary);
    }
</style>
