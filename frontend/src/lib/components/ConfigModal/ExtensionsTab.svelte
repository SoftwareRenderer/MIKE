<script lang="ts">
    import type { Extension, RegistryEntry, ExtensionUpdate } from '$lib/types';
    import { listExtensions, getRegistryIndex, installExtension, deleteExtension, setExtensionEnabled, checkUpdates, updateExtension, updateExtensionImage, createExtension, streamBuildProgress } from '$lib/api';
    import type { BuildProgressEvent } from '$lib/api';
    import { getConfig } from '$lib/api';
    import ToggleSwitch from '../ToggleSwitch.svelte';
    import { dialogStore } from '$lib/stores';

    let { onExtensionsChanged }: { onExtensionsChanged?: () => void } = $props();

    // State
    let extensions = $state<Extension[]>([]);
    let registryEntries = $state<RegistryEntry[]>([]);
    let updates = $state<ExtensionUpdate[]>([]);
    let registryUrl = $state('');
    let activeView = $state<'installed' | 'browse'>('installed');
    let loading = $state(false);
    let loadingRegistry = $state(false);
    let loadingUpdates = $state(false);
    let error = $state('');
    let searchQuery = $state('');

    // Extension form state
    let showCreateForm = $state(false);
    let showAddForm = $state(false);
    let editExtensionId = $state<string | null>(null);
    let editingExtension = $state<Extension | null>(null);
    let editConfig = $state<Record<string, any>>({});
    let saving = $state(false);
    let installing = $state<Set<string>>(new Set());
    let updating = $state<Set<string>>(new Set());
    let buildProgress = $state<{ ext: string; progress: number; message: string } | null>(null);

    // Add extension form
    let addForm = $state({
        name: '',
        type: 'lsp' as 'lsp' | 'ui',
        version: '1.0.0',
        displayName: '',
        description: '',
        author: '',
        licenseUrl: '',
        language: '',
        fileExtensions: '',
    });
    let adding = $state(false);

    // Reset add form
    function resetAddForm() {
        addForm = {
            name: '',
            type: 'lsp',
            version: '1.0.0',
            displayName: '',
            description: '',
            author: '',
            licenseUrl: '',
            language: '',
            fileExtensions: '',
        };
    }

    // Handle add extension
    async function handleAdd() {
        if (!addForm.name || !addForm.displayName) {
            error = 'Name and display name are required';
            return;
        }
        adding = true;
        error = '';
        try {
            const manifest: Record<string, any> = {
                display_name: addForm.displayName,
                description: addForm.description,
                author: addForm.author,
                license_url: addForm.licenseUrl,
            };
            if (addForm.type === 'lsp') {
                manifest.lsp = {
                    command: addForm.name,
                    extensions: addForm.fileExtensions ? addForm.fileExtensions.split(',').map(s => s.trim()).filter(Boolean) : [],
                    language: addForm.language || addForm.name,
                };
            }
            await createExtension({
                name: addForm.name,
                version: addForm.version,
                type: addForm.type,
                manifest,
            });
            showAddForm = false;
            resetAddForm();
            await loadExtensions();
        } catch (e: any) {
            error = e.message || 'Failed to add extension';
        } finally {
            adding = false;
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
            error = e.message || 'Failed to load extensions';
        } finally {
            loading = false;
        }
    }

    // Load registry
    async function loadRegistry() {
        loadingRegistry = true;
        error = '';
        try {
            registryEntries = await getRegistryIndex(registryUrl || undefined);
        } catch (e: any) {
            error = e.message || 'Failed to load registry';
        } finally {
            loadingRegistry = false;
        }
    }

    // Load config for registry URL
    async function loadConfig() {
        try {
            const config = await getConfig();
            registryUrl = config.registry_url || '';
        } catch {
            // Ignore config load errors
        }
    }

    // Install from registry
    async function handleInstall(name: string) {
        if (installing.has(name)) return;
        error = '';
        installing = new Set(installing).add(name);
        buildProgress = { ext: name, progress: 0, message: 'Installing...' };
        try {
            await installExtension(name, registryUrl || undefined);
            buildProgress = { ext: name, progress: 100, message: 'Installed!' };
            await loadExtensions();
            setTimeout(() => buildProgress = null, 2000);
        } catch (e: any) {
            error = e.message || 'Failed to install extension';
            buildProgress = null;
        } finally {
            const next = new Set(installing);
            next.delete(name);
            installing = next;
        }
    }

    // Stream build progress for an extension
    async function watchBuildProgress(name: string) {
        try {
            for await (const event of streamBuildProgress(name)) {
                buildProgress = { ext: event.extension, progress: event.progress, message: event.message };
                if (event.done) {
                    if (event.error) {
                        error = `Build failed: ${event.error}`;
                    }
                    setTimeout(() => buildProgress = null, 3000);
                }
            }
        } catch (e: any) {
            // Connection closed or error
            console.log('Build progress stream ended:', e.message);
        }
    }

    // Delete extension
    async function handleDelete(id: string, name: string) {
        const confirmed = await dialogStore.showConfirm({
            title: `Delete "${name}"?`,
            message: `This will remove the extension and stop its sandbox. This cannot be undone.`,
            confirmText: 'Delete',
            cancelText: 'Cancel',
        });
        if (!confirmed) return;

        error = '';
        try {
            await deleteExtension(id);
            await loadExtensions();
        } catch (e: any) {
            error = e.message || 'Failed to delete extension';
        }
    }

    // Toggle enabled
    async function handleToggleEnabled(id: string, enabled: boolean) {
        error = '';
        try {
            await setExtensionEnabled(id, enabled);
            await loadExtensions();
        } catch (e: any) {
            error = e.message || 'Failed to update extension';
        }
    }

    // Check for updates
    async function handleCheckUpdates() {
        loadingUpdates = true;
        error = '';
        try {
            updates = await checkUpdates(registryUrl || undefined);
        } catch (e: any) {
            error = e.message || 'Failed to check for updates';
        } finally {
            loadingUpdates = false;
        }
    }

    // Update an extension
    async function handleUpdate(id: string) {
        error = '';
        try {
            await updateExtension(id, {});
            await loadExtensions();
            updates = updates.filter(u => u.id !== id);
        } catch (e: any) {
            error = e.message || 'Failed to update extension';
        }
    }

    // Update extension image from registry (rebuild or pull)
    async function handleUpdateImage(id: string, name: string) {
        const confirmed = await dialogStore.showConfirm({
            title: 'Update Image',
            message: `Fetch the latest image for ${name}? The sandbox will be restarted.`,
            confirmText: 'Update',
        });
        if (!confirmed) return;

        error = '';
        updating = new Set(updating).add(name);
        buildProgress = { ext: name, progress: 0, message: 'Updating...' };
        try {
            await updateExtensionImage(id);
            buildProgress = { ext: name, progress: 100, message: 'Updated!' };
            setTimeout(() => buildProgress = null, 2000);
            await loadExtensions();
        } catch (e: any) {
            error = e.message || 'Failed to update extension';
            buildProgress = null;
        } finally {
            const next = new Set(updating);
            next.delete(name);
            updating = next;
        }
    }

    // Open edit form
    function openEdit(ext: Extension) {
        editingExtension = ext;
        editExtensionId = ext.id;
        editConfig = { ...ext.config };
    }

    // Close edit form
    function closeEdit() {
        editingExtension = null;
        editExtensionId = null;
        editConfig = {};
    }

    // Save edit
    async function handleSave() {
        if (!editExtensionId) return;
        saving = true;
        error = '';
        try {
            await updateExtension(editExtensionId, { config: editConfig });
            closeEdit();
            await loadExtensions();
        } catch (e: any) {
            error = e.message || 'Failed to save extension';
        } finally {
            saving = false;
        }
    }

    // Get config schema fields for an extension
    function getConfigSchemaFields(ext: Extension): Array<{ key: string; title: string; type: string; default?: any }> {
        const schema = ext.manifest.config_schema;
        if (!schema || !schema.properties) return [];
        return Object.entries(schema.properties).map(([key, val]: [string, any]) => ({
            key,
            title: val.title || key,
            type: val.type || 'string',
            default: val.default,
        }));
    }

    // Format built_at timestamp as a readable relative time
    function formatBuiltAt(builtAt: string): string {
        if (!builtAt) return 'Not built';
        const date = new Date(builtAt);
        if (isNaN(date.getTime())) return builtAt;
        const now = new Date();
        const diffMs = now.getTime() - date.getTime();
        const diffMins = Math.floor(diffMs / 60000);
        const diffHours = Math.floor(diffMs / 3600000);
        const diffDays = Math.floor(diffMs / 86400000);
        if (diffMins < 1) return 'Built just now';
        if (diffMins < 60) return `Built ${diffMins}m ago`;
        if (diffHours < 24) return `Built ${diffHours}h ago`;
        if (diffDays < 7) return `Built ${diffDays}d ago`;
        return `Built ${date.toLocaleDateString()}`;
    }

    // Filter extensions by search
    let filteredExtensions = $derived.by(() => {
        if (!searchQuery) return extensions;
        const q = searchQuery.toLowerCase();
        return extensions.filter(ext =>
            ext.name.toLowerCase().includes(q) ||
            ext.manifest.display_name.toLowerCase().includes(q) ||
            ext.type.includes(q)
        );
    });

    let filteredRegistry = $derived.by(() => {
        if (!searchQuery) return registryEntries;
        const q = searchQuery.toLowerCase();
        return registryEntries.filter(entry =>
            entry.name.toLowerCase().includes(q) ||
            entry.display_name.toLowerCase().includes(q) ||
            entry.description.toLowerCase().includes(q)
        );
    });

    // Initialize on mount
    $effect(() => {
        (async () => {
            await loadConfig();
            await loadExtensions();
        })();
    });
</script>

<div class="extensions-tab">
    <!-- Build Progress -->
    {#if buildProgress}
        <div class="build-progress">
            <div class="progress-bar" style="--pct: {buildProgress.progress}">
                <div class="progress-fill"></div>
            </div>
            <span class="progress-text">{buildProgress.message} ({buildProgress.ext})</span>
        </div>
    {/if}

    {#if error}
        <div class="error-banner" onclick={() => error = ''}>
            {error}
            <button class="dismiss" aria-label="Dismiss">\u2715</button>
        </div>
    {/if}

    <!-- View Tabs -->
    <div class="view-tabs">
        <button
            class="view-tab"
            class:active={activeView === 'installed'}
            onclick={() => activeView = 'installed'}
        >
            Installed ({extensions.length})
        </button>
        <button
            class="view-tab"
            class:active={activeView === 'browse'}
            onclick={() => { activeView = 'browse'; if (registryEntries.length === 0) loadRegistry(); }}
        >
            Browse Registry
        </button>
    </div>

    <!-- Search -->
    <div class="search-row">
        <input
            type="search"
            placeholder="Search extensions..."
            value={searchQuery}
            oninput={(e) => searchQuery = (e.target as HTMLInputElement).value}
            class="search-input"
        />
    </div>

    <!-- Installed View -->
    {#if activeView === 'installed'}
        <div class="installed-view">
            <!-- Actions Bar -->
            <div class="actions-bar">
                <button class="btn btn-primary" onclick={() => { showAddForm = true; resetAddForm(); }}>
                    + Add Extension
                </button>
                <button class="btn btn-secondary" onclick={handleCheckUpdates} disabled={loadingUpdates}>
                    {loadingUpdates ? 'Checking...' : 'Check for Updates'}
                </button>
            </div>

            <!-- Updates Available -->
            {#if updates.length > 0}
                <div class="updates-section">
                    <h3>Updates Available</h3>
                    {#each updates as update}
                        <div class="update-item">
                            <div class="update-info">
                                <span class="update-name">{update.name}</span>
                                <span class="update-versions">{update.current_version} \u2192 {update.available_version}</span>
                            </div>
                            <button class="btn btn-primary btn-small" onclick={() => handleUpdate(update.id)}>Update</button>
                        </div>
                    {/each}
                </div>
            {/if}

            <!-- Extension List -->
            {#if loading}
                <div class="loading">Loading extensions...</div>
            {:else if filteredExtensions.length === 0}
                <div class="empty-state">
                    <p>No extensions installed</p>
                    <p class="help">Install extensions from the registry to add LSP servers and UI plugins.</p>
                    <button class="btn btn-primary" onclick={() => { activeView = 'browse'; loadRegistry(); }}>
                        Browse Registry
                    </button>
                </div>
            {:else}
                <div class="extension-list">
                    {#each filteredExtensions as ext}
                        <div class="extension-card" class:disabled={!ext.enabled}>
                            <div class="card-header">
                                <div class="ext-icon" class:ext-lsp={ext.type === 'lsp'} class:ext-ui={ext.type === 'ui'}>
                                    {ext.type === 'lsp' ? 'LSP' : 'UI'}
                                </div>
                                <div class="ext-info">
                                    <span class="ext-name">{ext.manifest.display_name || ext.name}</span>
                                    <span class="ext-meta">
                                        {formatBuiltAt(ext.built_at)}
                                        {#if ext.manifest.author} · {ext.manifest.author}{/if}
                                        {#if ext.manifest.license_url} · <a href={ext.manifest.license_url} target="_blank" rel="noopener noreferrer" class="ext-link">License</a>{/if}
                                    </span>
                                </div>
                                <div class="ext-status">
                                    {#if ext.running}
                                        <span class="status-badge running">Running</span>
                                    {:else if ext.enabled}
                                        <span class="status-badge stopped">Stopped</span>
                                    {:else}
                                        <span class="status-badge disabled">Disabled</span>
                                    {/if}
                                </div>
                            </div>
                            {#if ext.manifest.description}
                                <p class="ext-description">{ext.manifest.description}</p>
                            {/if}
                            <div class="card-actions">
                                <label class="toggle-label">
                                    <ToggleSwitch checked={ext.enabled} onChange={() => handleToggleEnabled(ext.id, !ext.enabled)} />
                                    <span>Enabled</span>
                                </label>
                                <button class="btn btn-icon" title="Edit" onclick={() => openEdit(ext)}>
                                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                                        <path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"/>
                                        <path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"/>
                                    </svg>
                                </button>
                                <button class="btn btn-icon" title="Update Image" onclick={() => handleUpdateImage(ext.id, ext.name)} disabled={updating.has(ext.name)}>
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
                                <button class="btn btn-icon btn-danger" title="Delete" onclick={() => handleDelete(ext.id, ext.name)}>
                                    <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                                        <path d="M2 4h12M5 4V3a1 1 0 011-1h4a1 1 0 011 1v1M6 7v5M10 7v5" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                                        <path d="M3 4l1 9a1 1 0 001 1h6a1 1 0 001-1l1-9" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                                    </svg>
                                </button>
                            </div>
                        </div>
                    {/each}
                </div>
            {/if}
        </div>
    {/if}

    <!-- Add Extension Form Overlay -->
    {#if showAddForm}
        <div class="edit-overlay" onclick={() => { showAddForm = false; resetAddForm(); }}>
            <div class="edit-form" onclick={(e) => e.stopPropagation()}>
                <div class="edit-header">
                    <h3>Add Extension</h3>
                    <button class="btn btn-icon" onclick={() => { showAddForm = false; resetAddForm(); }}>&times;</button>
                </div>

                <div class="config-fields">
                    <div class="config-field">
                        <label class="field-label">Name *</label>
                        <input type="text" value={addForm.name} oninput={(e) => addForm.name = (e.target as HTMLInputElement).value} class="config-input" placeholder="gopls" />
                    </div>
                    <div class="config-field">
                        <label class="field-label">Display Name *</label>
                        <input type="text" value={addForm.displayName} oninput={(e) => addForm.displayName = (e.target as HTMLInputElement).value} class="config-input" placeholder="Go Language Server" />
                    </div>
                    <div class="config-field">
                        <label class="field-label">Type</label>
                        <select value={addForm.type} onchange={(e) => addForm.type = (e.target as HTMLSelectElement).value as 'lsp' | 'ui'} class="config-input">
                            <option value="lsp">LSP</option>
                            <option value="ui">UI</option>
                        </select>
                    </div>
                    <div class="config-field">
                        <label class="field-label">Description</label>
                        <input type="text" value={addForm.description} oninput={(e) => addForm.description = (e.target as HTMLInputElement).value} class="config-input" placeholder="Go language server" />
                    </div>
                    <div class="config-field">
                        <label class="field-label">Author</label>
                        <input type="text" value={addForm.author} oninput={(e) => addForm.author = (e.target as HTMLInputElement).value} class="config-input" placeholder="Go Team" />
                    </div>
                    <div class="config-field">
                        <label class="field-label">License URL</label>
                        <input type="url" value={addForm.licenseUrl} oninput={(e) => addForm.licenseUrl = (e.target as HTMLInputElement).value} class="config-input" placeholder="https://..." />
                    </div>
                    {#if addForm.type === 'lsp'}
                        <div class="config-field">
                            <label class="field-label">Language</label>
                            <input type="text" value={addForm.language} oninput={(e) => addForm.language = (e.target as HTMLInputElement).value} class="config-input" placeholder="go" />
                        </div>
                        <div class="config-field">
                            <label class="field-label">File Extensions (comma-separated)</label>
                            <input type="text" value={addForm.fileExtensions} oninput={(e) => addForm.fileExtensions = (e.target as HTMLInputElement).value} class="config-input" placeholder="go" />
                        </div>
                    {/if}
                </div>

                <div class="edit-actions">
                    <button class="btn btn-secondary" onclick={() => { showAddForm = false; resetAddForm(); }}>Cancel</button>
                    <button class="btn btn-primary" onclick={handleAdd} disabled={adding}>
                        {adding ? 'Adding...' : 'Add'}
                    </button>
                </div>
            </div>
        </div>
    {/if}

    <!-- Edit Form Overlay -->
    {#if editingExtension}
        <div class="edit-overlay" onclick={closeEdit}>
            <div class="edit-form" onclick={(e) => e.stopPropagation()}>
                <div class="edit-header">
                    <h3>Edit: {editingExtension.manifest.display_name || editingExtension.name}</h3>
                    <button class="btn btn-icon" onclick={closeEdit}>&times;</button>
                </div>

                <!-- Config Schema Fields -->
                {#if getConfigSchemaFields(editingExtension).length > 0}
                    <div class="config-fields">
                        <h4>Configuration</h4>
                        {#each getConfigSchemaFields(editingExtension) as field}
                            <div class="config-field">
                                <label class="field-label">{field.title}</label>
                                {#if field.type === 'number'}
                                    <input
                                        type="number"
                                        value={editConfig[field.key] ?? field.default ?? ''}
                                        oninput={(e) => editConfig[field.key] = Number((e.target as HTMLInputElement).value)}
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
                                        type="text"
                                        value={editConfig[field.key] ?? field.default ?? ''}
                                        oninput={(e) => editConfig[field.key] = (e.target as HTMLInputElement).value}
                                        class="config-input"
                                    />
                                {/if}
                            </div>
                        {/each}
                    </div>
                {:else}
                    <p class="no-schema">No configurable fields for this extension.</p>
                {/if}

                <div class="edit-actions">
                    <button class="btn btn-secondary" onclick={closeEdit}>Cancel</button>
                    <button class="btn btn-primary" onclick={handleSave} disabled={saving}>
                        {saving ? 'Saving...' : 'Save'}
                    </button>
                </div>
            </div>
        </div>
    {/if}

    <!-- Browse View -->
    {#if activeView === 'browse'}
        <div class="browse-view">
            <!-- Registry URL -->
            <div class="registry-url-row">
                <label class="field-label">Registry URL</label>
                <div class="url-input-row">
                    <input
                        type="url"
                        placeholder="https://registry.example.com/index.json"
                        value={registryUrl}
                        oninput={(e) => registryUrl = (e.target as HTMLInputElement).value}
                        class="url-input"
                    />
                    <button class="btn btn-secondary" onclick={loadRegistry} disabled={loadingRegistry}>
                        {loadingRegistry ? 'Loading...' : 'Refresh'}
                    </button>
                </div>
            </div>

            <!-- Registry List -->
            {#if loadingRegistry}
                <div class="loading">Loading registry...</div>
            {:else if filteredRegistry.length === 0}
                <div class="empty-state">
                    <p>No extensions in registry</p>
                    <p class="help">Check the registry URL or try a different source.</p>
                </div>
            {:else}
                <div class="extension-list">
                    {#each filteredRegistry as entry}
                        <div class="extension-card registry-card">
                            <div class="card-header">
                                <div class="ext-icon" class:ext-lsp={entry.type === 'lsp'} class:ext-ui={entry.type === 'ui'}>
                                    {entry.type === 'lsp' ? 'LSP' : 'UI'}
                                </div>
                                <div class="ext-info">
                                    <span class="ext-name">{entry.display_name || entry.name}</span>
                                    <span class="ext-meta">
                                        v{entry.version}
                                        {#if entry.author} · {entry.author}{/if}
                                        {#if entry.license_url} · <a href={entry.license_url} target="_blank" rel="noopener noreferrer" class="ext-link">License</a>{/if}
                                    </span>
                                </div>
                            </div>
                            {#if entry.description}
                                <p class="ext-description">{entry.description}</p>
                            {/if}
                            <div class="card-actions">
                                {#if extensions.some(e => e.name === entry.name)}
                                    <span class="installed-badge">Installed</span>
                                {:else}
                                    <button class="btn btn-primary" onclick={() => handleInstall(entry.name)} disabled={installing.has(entry.name)}>
                                        {#if installing.has(entry.name)}
                                            <span class="spinner"></span>
                                            Installing...
                                        {:else}
                                            Install
                                        {/if}
                                    </button>
                                {/if}
                            </div>
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

    /* Build progress */
    .build-progress {
        display: flex;
        align-items: center;
        gap: 1rem;
        background: rgba(88, 166, 255, 0.08);
        border: 1px solid rgba(88, 166, 255, 0.2);
        border-radius: 8px;
        padding: 0.75rem 1rem;
    }

    .progress-bar {
        flex: 1;
        height: 8px;
        background: rgba(88, 166, 255, 0.15);
        border-radius: 4px;
        overflow: hidden;
    }

    .progress-fill {
        height: 100%;
        width: var(--pct, 0);
        background: #58a6ff;
        border-radius: 4px;
        transition: width 0.3s ease;
    }

    .progress-text {
        font-size: 0.8rem;
        color: #58a6ff;
        white-space: nowrap;
    }

    .error-banner {
        background: rgba(248, 81, 73, 0.15);
        border: 1px solid rgba(248, 81, 73, 0.3);
        border-radius: 6px;
        padding: 0.75rem 1rem;
        color: #f85149;
        cursor: pointer;
        display: flex;
        justify-content: space-between;
        align-items: center;
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
        gap: 0.5rem;
        border-bottom: 1px solid #30363d;
        padding-bottom: 0.5rem;
    }

    .view-tab {
        background: none;
        border: none;
        color: #8b949e;
        cursor: pointer;
        font-size: 0.875rem;
        padding: 0.5rem 1rem;
        border-radius: 6px;
        transition: all 0.15s;
    }

    .view-tab:hover {
        background: rgba(139, 148, 158, 0.1);
        color: #c9d1d9;
    }

    .view-tab.active {
        background: rgba(88, 166, 255, 0.15);
        color: #58a6ff;
    }

    /* Search */
    .search-row {
        display: flex;
        gap: 0.5rem;
    }

    .search-input {
        flex: 1;
        background: #0d1117;
        border: 1px solid #30363d;
        border-radius: 6px;
        padding: 0.5rem 0.75rem;
        color: #c9d1d9;
        font-size: 0.875rem;
    }

    .search-input:focus {
        outline: none;
        border-color: #58a6ff;
    }

    /* Actions bar */
    .actions-bar {
        display: flex;
        gap: 0.5rem;
        justify-content: flex-end;
    }

    /* Updates section */
    .updates-section {
        background: rgba(88, 166, 255, 0.08);
        border: 1px solid rgba(88, 166, 255, 0.2);
        border-radius: 8px;
        padding: 1rem;
    }

    .updates-section h3 {
        margin: 0 0 0.75rem;
        font-size: 0.875rem;
        color: #58a6ff;
    }

    .update-item {
        display: flex;
        justify-content: space-between;
        align-items: center;
        padding: 0.5rem 0;
    }

    .update-item:not(:last-child) {
        border-bottom: 1px solid rgba(88, 166, 255, 0.15);
    }

    .update-name {
        font-weight: 600;
        color: #c9d1d9;
    }

    .update-versions {
        font-size: 0.8rem;
        color: #8b949e;
        margin-left: 0.5rem;
    }

    /* Extension list */
    .extension-list {
        display: flex;
        flex-direction: column;
        gap: 0.75rem;
        margin-top: 1rem;
    }

    .extension-card {
        background: #161b22;
        border: 1px solid #30363d;
        border-radius: 8px;
        padding: 1rem;
        transition: border-color 0.15s;
    }

    .extension-card:hover {
        border-color: #484f58;
    }

    .extension-card.disabled {
        opacity: 0.6;
    }

    .registry-card {
        border-style: dashed;
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
        color: #3fb950;
    }

    .ext-ui {
        background: rgba(163, 113, 247, 0.15);
        color: #a371f7;
    }

    .ext-info {
        flex: 1;
        min-width: 0;
    }

    .ext-name {
        display: block;
        font-weight: 600;
        color: #c9d1d9;
        font-size: 0.95rem;
    }

    .ext-meta {
        font-size: 0.8rem;
        color: #8b949e;
    }

    .ext-link {
        color: #58a6ff;
        text-decoration: none;
    }

    .ext-link:hover {
        text-decoration: underline;
    }

    .ext-status {
        flex-shrink: 0;
    }

    .status-badge {
        font-size: 0.75rem;
        padding: 0.2rem 0.5rem;
        border-radius: 999px;
        font-weight: 500;
    }

    .status-badge.running {
        background: rgba(63, 185, 80, 0.15);
        color: #3fb950;
    }

    .status-badge.stopped {
        background: rgba(139, 148, 158, 0.15);
        color: #8b949e;
    }

    .status-badge.disabled {
        background: rgba(248, 81, 73, 0.15);
        color: #f85149;
    }

    .ext-description {
        margin: 0 0 0.75rem;
        font-size: 0.85rem;
        color: #8b949e;
        line-height: 1.4;
    }

    .card-actions {
        display: flex;
        align-items: center;
        gap: 0.75rem;
    }

    .toggle-label {
        display: flex;
        align-items: center;
        gap: 0.5rem;
        font-size: 0.85rem;
        color: #8b949e;
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
        background: #238636;
        color: #fff;
    }

    .btn-primary:hover:not(:disabled) {
        background: #2ea043;
    }

    .btn-secondary {
        background: #21262d;
        border-color: #30363d;
        color: #c9d1d9;
    }

    .btn-secondary:hover:not(:disabled) {
        background: #30363d;
    }

    .btn-icon {
        background: none;
        border: none;
        color: #8b949e;
        padding: 0.25rem;
        font-size: 1rem;
    }

    .btn-icon:hover {
        color: #c9d1d9;
    }

    .btn-danger:hover {
        color: #f85149;
    }

    .btn-small {
        padding: 0.25rem 0.5rem;
        font-size: 0.75rem;
    }

    .installed-badge {
        font-size: 0.8rem;
        color: #3fb950;
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
        border-top-color: #fff;
        border-radius: 50%;
        animation: spin 0.6s linear infinite;
        vertical-align: middle;
        margin-right: 0.35rem;
    }

    @keyframes spin {
        to { transform: rotate(360deg); }
    }

    /* Registry URL */
    .registry-url-row {
        margin-bottom: 1rem;
    }

    .field-label {
        display: block;
        font-size: 0.8rem;
        color: #8b949e;
        margin-bottom: 0.25rem;
    }

    .url-input-row {
        display: flex;
        gap: 0.5rem;
    }

    .url-input {
        flex: 1;
        background: #0d1117;
        border: 1px solid #30363d;
        border-radius: 6px;
        padding: 0.5rem 0.75rem;
        color: #c9d1d9;
        font-size: 0.875rem;
    }

    .url-input:focus {
        outline: none;
        border-color: #58a6ff;
    }

    /* Empty state */
    .empty-state {
        text-align: center;
        padding: 3rem 1rem;
        color: #8b949e;
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
        color: #8b949e;
    }

    /* Edit form overlay */
    .edit-overlay {
        position: fixed;
        inset: 0;
        background: rgba(0, 0, 0, 0.6);
        display: flex;
        align-items: center;
        justify-content: center;
        z-index: 100;
    }

    .edit-form {
        background: #161b22;
        border: 1px solid #30363d;
        border-radius: 12px;
        padding: 1.5rem;
        width: 90%;
        max-width: 480px;
        max-height: 80vh;
        overflow-y: auto;
    }

    .edit-header {
        display: flex;
        justify-content: space-between;
        align-items: center;
        margin-bottom: 1.5rem;
    }

    .edit-header h3 {
        margin: 0;
        font-size: 1.1rem;
        color: #c9d1d9;
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
        color: #8b949e;
        text-transform: uppercase;
        letter-spacing: 0.05em;
    }

    .config-field {
        display: flex;
        flex-direction: column;
        gap: 0.25rem;
    }

    .config-input {
        background: #0d1117;
        border: 1px solid #30363d;
        border-radius: 6px;
        padding: 0.5rem 0.75rem;
        color: #c9d1d9;
        font-size: 0.875rem;
    }

    .config-input:focus {
        outline: none;
        border-color: #58a6ff;
    }

    .no-schema {
        color: #8b949e;
        font-size: 0.85rem;
        margin-bottom: 1.5rem;
    }

    .edit-actions {
        display: flex;
        gap: 0.5rem;
        justify-content: flex-end;
    }
</style>
