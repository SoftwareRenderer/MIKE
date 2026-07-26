<script lang="ts">
    import type { Extension, RegistryEntry, ExtensionUpdate, SandboxMount, SandboxEnv } from '$lib/types';
    import { listExtensions, getRegistryIndex, installExtension, deleteExtension, setExtensionEnabled, checkUpdates, updateExtension, updateExtensionImage, createExtension, streamBuildProgress } from '$lib/api';
    import type { BuildProgressEvent } from '$lib/api';
    import { getConfig } from '$lib/api';
    import ToggleSwitch from '../ToggleSwitch.svelte';
    import BaseModal from '../BaseModal.svelte';
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
        type: 'lsp' as 'lsp' | 'ui' | 'css',
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
            } else if (addForm.type === 'css') {
                manifest.css = {
                    css_url: `/extensions/${addForm.name}/styles.css`,
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
    function getConfigSchemaFields(ext: Extension): Array<{ key: string; title: string; type: string; default?: any; sensitive?: boolean; enum?: any[] }> {
        const schema = ext.manifest.config_schema;
        if (!schema || !schema.properties) return [];
        return schema.properties.map((p: SchemaProperty) => ({
            key: p.key,
            title: p.title || p.key,
            type: p.type || 'string',
            default: p.default,
            sensitive: p.sensitive || false,
            enum: p.enum,
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
        <div class="error-banner" role="button" tabindex="0" onclick={() => error = ''} onkeydown={(e) => e.key === 'Enter' && (error = '')}>
            {error}
            <button class="dismiss" aria-label="Dismiss">\u2715</button>
        </div>
    {/if}

    <!-- View Tabs -->
    <div class="view-tabs">
        <div class="view-tabs-left">
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
        <div class="view-tabs-right">
            <button class="btn btn-primary" onclick={() => { showAddForm = true; resetAddForm(); }}>
                + Add Extension
            </button>
            <button class="btn btn-secondary" onclick={handleCheckUpdates} disabled={loadingUpdates}>
                {loadingUpdates ? 'Checking...' : 'Check for Updates'}
            </button>
        </div>
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
                                <div class="ext-icon" class:ext-lsp={ext.type === 'lsp'} class:ext-ui={ext.type === 'ui'} class:ext-css={ext.type === 'css'} class:ext-agent={ext.type === 'agent'}>
                                    {ext.type === 'lsp' ? 'LSP' : ext.type === 'css' ? 'CSS' : ext.type === 'agent' ? 'AGENT' : 'UI'}
                                </div>
                                <div class="ext-info">
                                    <span class="ext-name">{ext.manifest.display_name || ext.name}</span>
                                    <span class="ext-meta">
                                        {#if ext.type === 'ui' || ext.type === 'css'}
                                            v{ext.version}
                                        {:else}
                                            {formatBuiltAt(ext.built_at)}
                                        {/if}
                                        {#if ext.manifest.author}
                                            · {#if ext.manifest.homepage}<a href={ext.manifest.homepage} target="_blank" rel="noopener noreferrer" class="ext-link">{ext.manifest.author}</a>{:else}{ext.manifest.author}{/if}
                                        {/if}
                                        {#if ext.manifest.license_url} · <a href={ext.manifest.license_url} target="_blank" rel="noopener noreferrer" class="ext-link">License</a>{/if}
                                    </span>
                                </div>
                                <div class="card-actions">
                                    <button class="btn btn-icon btn-danger" title="Delete" onclick={() => handleDelete(ext.id, ext.name)}>
                                        <svg width="16" height="16" viewBox="0 0 16 16" fill="none" aria-hidden="true">
                                            <path d="M2 4h12M5 4V3a1 1 0 011-1h4a1 1 0 011 1v1M6 7v5M10 7v5" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                                            <path d="M3 4l1 9a1 1 0 001 1h6a1 1 0 001-1l1-9" stroke="currentColor" stroke-width="1.3" stroke-linecap="round" stroke-linejoin="round"/>
                                        </svg>
                                    </button>
                                    {#if ext.type === 'lsp'}
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
                                    {/if}
                                    {#if ext.manifest.config_schema}
                                    <button class="btn btn-icon" title="Edit" onclick={() => openEdit(ext)}>
                                        <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
                                            <path d="M11 4H4a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2v-7"/>
                                            <path d="M18.5 2.5a2.121 2.121 0 0 1 3 3L12 15l-4 1 1-4 9.5-9.5z"/>
                                        </svg>
                                    </button>
                                    {/if}
                                    <ToggleSwitch class="card-toggle" checked={ext.enabled} onChange={() => handleToggleEnabled(ext.id, !ext.enabled)} />
                                </div>
                            </div>
                            {#if ext.manifest.description}
                                <p class="ext-description">{ext.manifest.description}</p>
                            {/if}
                            {#if ext.type === 'agent'}
                                <p class="ext-note">
                                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="12" r="10"/><line x1="12" y1="16" x2="12" y2="12"/><line x1="12" y1="8" x2="12.01" y2="8"/></svg>
                                    This will add {ext.manifest.display_name} as an option in <code>Providers</code>
                                </p>
                            {/if}
                        </div>
                    {/each}
                </div>
            {/if}
        </div>
    {/if}

    <!-- Add Extension Modal -->
    {#if showAddForm}
        {#snippet addFooter()}
            <button type="button" class="btn-secondary" onclick={() => { showAddForm = false; resetAddForm(); }}>Cancel</button>
            <button type="button" class="btn-primary" onclick={handleAdd} disabled={adding}>
                {adding ? 'Adding...' : 'Add'}
            </button>
        {/snippet}

        <BaseModal title="Add Extension" width="480px" footer={addFooter} onClose={() => { showAddForm = false; resetAddForm(); }}>
            <div class="config-fields">
                <div class="config-field">
                    <label class="field-label" for="add-name">Name *</label>
                    <input id="add-name" type="text" value={addForm.name} oninput={(e) => addForm.name = (e.target as HTMLInputElement).value} class="config-input" placeholder="gopls" />
                </div>
                <div class="config-field">
                    <label class="field-label" for="add-display-name">Display Name *</label>
                    <input id="add-display-name" type="text" value={addForm.displayName} oninput={(e) => addForm.displayName = (e.target as HTMLInputElement).value} class="config-input" placeholder="Go Language Server" />
                </div>
                <div class="config-field">
                    <label class="field-label" for="add-type">Type</label>
                    <select id="add-type" value={addForm.type} onchange={(e) => addForm.type = (e.target as HTMLSelectElement).value as 'lsp' | 'ui' | 'css'} class="config-input">
                        <option value="lsp">LSP</option>
                        <option value="ui">UI</option>
                        <option value="css">CSS</option>
                    </select>
                </div>
                <div class="config-field">
                    <label class="field-label" for="add-description">Description</label>
                    <input id="add-description" type="text" value={addForm.description} oninput={(e) => addForm.description = (e.target as HTMLInputElement).value} class="config-input" placeholder="Go language server" />
                </div>
                <div class="config-field">
                    <label class="field-label" for="add-author">Author</label>
                    <input id="add-author" type="text" value={addForm.author} oninput={(e) => addForm.author = (e.target as HTMLInputElement).value} class="config-input" placeholder="Go Team" />
                </div>
                <div class="config-field">
                    <label class="field-label" for="add-license-url">License URL</label>
                    <input id="add-license-url" type="url" value={addForm.licenseUrl} oninput={(e) => addForm.licenseUrl = (e.target as HTMLInputElement).value} class="config-input" placeholder="https://..." />
                </div>
                {#if addForm.type === 'lsp'}
                    <div class="config-field">
                        <label class="field-label" for="add-language">Language</label>
                        <input id="add-language" type="text" value={addForm.language} oninput={(e) => addForm.language = (e.target as HTMLInputElement).value} class="config-input" placeholder="go" />
                    </div>
                    <div class="config-field">
                        <label class="field-label" for="add-file-extensions">File Extensions (comma-separated)</label>
                        <input id="add-file-extensions" type="text" value={addForm.fileExtensions} oninput={(e) => addForm.fileExtensions = (e.target as HTMLInputElement).value} class="config-input" placeholder="go" />
                    </div>
                {/if}
            </div>
        </BaseModal>
    {/if}

    <!-- Edit Extension Modal -->
    {#if editingExtension}
        {#snippet editFooter()}
            <button type="button" class="btn-secondary" onclick={closeEdit}>Cancel</button>
            <button type="button" class="btn-primary" onclick={handleSave} disabled={saving}>
                {saving ? 'Saving...' : 'Save'}
            </button>
        {/snippet}

        <BaseModal title={`Edit: ${editingExtension.manifest.display_name || editingExtension.name}`} width="480px" footer={editFooter} onClose={closeEdit}>
            <!-- Config Schema Fields -->
            {#if getConfigSchemaFields(editingExtension).length > 0}
                <div class="config-fields">
                    <h4>Configuration</h4>
                    {#each getConfigSchemaFields(editingExtension) as field}
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
                                                    <button type="button" class="btn-remove" onclick={() => { editConfig[field.key] = mounts.filter((_, j) => j !== i); }} title="Remove"><svg width="12" height="12" viewBox="0 0 12 12" fill="none"><path d="M3 3L9 9M9 3L3 9" stroke="currentColor" stroke-width="1.3" stroke-linecap="round"/></svg></button>
                                                </div>
                                            {/each}
                                        </div>
                                    {/if}
                                    <button type="button" class="btn-add" onclick={() => { editConfig[field.key] = [...mounts, { host_path: '', container_path: '', mode: 'rw' as const }]; }} title="Add mount"><svg width="14" height="14" viewBox="0 0 14 14" fill="none"><path d="M7 2V12M2 7H12" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/></svg></button>
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
                                                    <button type="button" class="btn-remove" onclick={() => { editConfig[field.key] = envs.filter((_, j) => j !== i); }} title="Remove"><svg width="12" height="12" viewBox="0 0 12 12" fill="none"><path d="M3 3L9 9M9 3L3 9" stroke="currentColor" stroke-width="1.3" stroke-linecap="round"/></svg></button>
                                                </div>
                                            {/each}
                                        </div>
                                    {/if}
                                    <button type="button" class="btn-add" onclick={() => { editConfig[field.key] = [...envs, { key: '', value: '' }]; }} title="Add variable"><svg width="14" height="14" viewBox="0 0 14 14" fill="none"><path d="M7 2V12M2 7H12" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/></svg></button>
                                </div>
                            {:else if field.type === 'number'}
                                <input
                                    id="edit-config-{field.key}"
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
            {:else}
                <p class="no-schema">No configurable fields for this extension.</p>
            {/if}
        </BaseModal>
    {/if}

    <!-- Browse View -->
    {#if activeView === 'browse'}
        <div class="browse-view">
            <!-- Registry URL -->
            <div class="registry-url-row">
                <label class="field-label" for="registry-url">Registry URL</label>
                <div class="url-input-row">
                    <input
                        id="registry-url"
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
                                <div class="ext-icon" class:ext-lsp={entry.type === 'lsp'} class:ext-ui={entry.type === 'ui'} class:ext-css={entry.type === 'css'} class:ext-agent={entry.type === 'agent'}>
                                    {entry.type === 'lsp' ? 'LSP' : entry.type === 'css' ? 'CSS' : entry.type === 'agent' ? 'AGENT' : 'UI'}
                                </div>
                                <div class="ext-info">
                                    <span class="ext-name">{entry.display_name || entry.name}</span>
                                    <span class="ext-meta">
                                        v{entry.version}
                                        {#if entry.author}
                                            · {#if entry.homepage}<a href={entry.homepage} target="_blank" rel="noopener noreferrer" class="ext-link">{entry.author}</a>{:else}{entry.author}{/if}
                                        {/if}
                                        {#if entry.license_url} · <a href={entry.license_url} target="_blank" rel="noopener noreferrer" class="ext-link">License</a>{/if}
                                    </span>
                                </div>
                                <div class="ext-action">
                                    {#if extensions.some(e => e.name === entry.name)}
                                        <span class="installed-badge">Installed</span>
                                    {:else}
                                        <button class="btn btn-primary btn-small" onclick={() => handleInstall(entry.name)} disabled={installing.has(entry.name)}>
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
                            {#if entry.description}
                                <p class="ext-description">{entry.description}</p>
                            {/if}
                            {#if entry.type === 'agent'}
                                <p class="ext-note">
                                    <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="12" r="10"/><line x1="12" y1="16" x2="12" y2="12"/><line x1="12" y1="8" x2="12.01" y2="8"/></svg>
                                    This will add {entry.display_name} as an option in <code>Providers</code>
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
        background: var(--color-accent);
        border-radius: 4px;
        transition: width 0.3s ease;
    }

    .progress-text {
        font-size: 0.8rem;
        color: var(--color-accent);
        white-space: nowrap;
    }

    .error-banner {
        background: rgba(248, 81, 73, 0.15);
        border: 1px solid rgba(248, 81, 73, 0.3);
        border-radius: 6px;
        padding: 0.75rem 1rem;
        color: var(--color-status-red);
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
        align-items: center;
        justify-content: space-between;
        gap: 0.5rem;
        border-bottom: 1px solid var(--color-border-subtle);
        padding-bottom: 0.5rem;
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
        margin: 0 0 0.75rem;
        font-size: 0.875rem;
        color: var(--color-accent);
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
        color: var(--color-text-primary);
    }

    .update-versions {
        font-size: 0.8rem;
        color: var(--color-text-tertiary);
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

    .ext-info {
        flex: 1;
        min-width: 0;
    }

    .ext-name {
        display: block;
        font-weight: 600;
        color: var(--color-text-primary);
        font-size: 0.95rem;
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

    /* Registry URL */
    .registry-url-row {
        margin-bottom: 1rem;
    }

    .field-label {
        display: block;
        font-size: 0.8rem;
        color: var(--color-text-tertiary);
        margin-bottom: 0.25rem;
    }

    .url-input-row {
        display: flex;
        gap: 0.5rem;
    }

    .url-input {
        flex: 1;
        background: var(--color-bg-input);
        border: 1px solid var(--color-border-subtle);
        border-radius: 6px;
        padding: 0.5rem 0.75rem;
        color: var(--color-text-primary);
        font-size: 0.875rem;
    }

    .url-input:focus {
        outline: none;
        border-color: var(--color-accent);
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
