<script lang="ts">
    import type { Extension, RegistryEntry, ExtensionUpdate, SandboxMount, SandboxEnv, SchemaProperty } from '$lib/types';
    import { listExtensions, getRegistryIndex, installExtension, deleteExtension, setExtensionEnabled, checkUpdates, updateExtension, updateExtensionImage } from '$lib/api';
    import ToggleSwitch from '../ToggleSwitch.svelte';
    import BaseModal from '../BaseModal.svelte';
    import { dialogStore } from '$lib/stores';

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

    // State
    let extensions = $state<Extension[]>([]);
    let registryEntries = $state<RegistryEntry[]>([]);
    let updates = $state<ExtensionUpdate[]>([]);
    let activeView = $state<'installed' | 'browse'>('installed');
    let loading = $state(false);
    let loadingRegistry = $state(false);
    let loadingUpdates = $state(false);
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

    // Human-readable labels for UI injection points
    const INJECTION_POINT_LABELS: Record<string, string> = {
        'taskCard': 'Task Cards',
        'fileEditor': 'File Editor',
        'projectBoard': 'Project Board',
        'taskChat': 'Task Chat',
        'projectSidebar': 'Project Sidebar',
        'fileBrowser': 'File Browser',
        'createTask': 'Create Task',
    };

    // Capability descriptions
    const CAPABILITY_DESCRIPTIONS: Record<string, { label: string; description: string }> = {
        proxy: {
            label: 'Proxy',
            description: 'Enables external API access for specific domains.',
        },
        lsp: {
            label: 'LSP',
            description: 'Provides a Language Server Protocol server for code intelligence — completions, diagnostics, go-to-definition, and more. Runs in an isolated gVisor sandbox.',
        },
        ui: {
            label: 'UI',
            description: 'Customizes interactive elements. The component runs within a sandboxed iframe.',
        },
        css: {
            label: 'CSS',
            description: 'Loads a global stylesheet that applies custom styling across the entire application. Used for themes and visual customizations.',
        },
        agent: {
            label: 'Agent',
            description: 'Adds an AI coding agent as a provider option.',
        },
    };

    // Human-readable explanations for sandboxed UI extension permissions.
    // Non-technical, user-facing wording. Unknown permission names fall back
    // to the raw name in the UI.
    const PERMISSION_DESCRIPTIONS: Record<string, { label: string; description: string }> = {
        'createTask.description.insertText': {
            label: 'Edit the task description',
            description: 'Can insert text into the task description field.',
        },
        'createTask.events.paste': {
            label: 'See what you paste',
            description: 'Can see text pasted into the task description so it can act on it.',
        },
        'network.proxy': {
            label: 'Reach external services',
            description: 'Can make requests to external services through the server, limited to the allowed domains listed below.',
        },
        'config.get': {
            label: 'Read its own settings',
            description: 'Can read the settings you configured for this extension.',
        },
        'log.write': {
            label: 'Write to the console',
            description: 'Can write diagnostic entries to the browser developer console.',
        },
        'ui.status': {
            label: 'Show loading/error indicators',
            description: 'Can show a spinner, error message, or hide its status indicator.',
        },
        'ui.setSize': {
            label: 'Resize its own panel',
            description: 'Can change the height of the space it occupies on the page.',
        },
        'storage.get': {
            label: 'Read its saved data',
            description: 'Can read data it previously saved for the current task or project.',
        },
        'storage.set': {
            label: 'Save its own data',
            description: 'Can save small amounts of data for the current task or project.',
        },
        'storage.delete': {
            label: 'Delete its saved data',
            description: 'Can remove data it previously saved.',
        },
        'storage.list': {
            label: 'List its saved data',
            description: 'Can list the data it saved for the current task or project.',
        },
        'editor.addFooterButton': {
            label: 'Add a button to the editor',
            description: 'Can add a button to the bottom toolbar of the code editor.',
        },
        'editor.removeFooterButton': {
            label: 'Remove its editor button',
            description: 'Can remove a button it previously added to the code editor toolbar.',
        },
        'editor.getContent': {
            label: 'Read the open file',
            description: 'Can read the full contents of the file currently open in the editor.',
        },
        'editor.setContent': {
            label: 'Write to the open file',
            description: 'Can write the full contents of the file currently open in the editor.',
        },
        'editor.setOverlay': {
            label: 'Replace the editor view',
            description: 'Can hide the code editor and show its own view in its place, and restore the editor afterwards.',
        },
        'editor.contentChange': {
            label: 'See file edits',
            description: 'Can receive updates when the content of the open file changes.',
        },
    };

    // Get list of capability keys for an installed extension
    function getCapabilityKeys(ext: Extension): string[] {
        const caps = ext.manifest.capabilities;
        if (!caps) return [];
        const keys: string[] = [];
        if (caps.lsp) keys.push('lsp');
        if (caps.ui) keys.push('ui');
        if (caps.css) keys.push('css');
        if (caps.agent) keys.push('agent');
        if (caps.proxy) keys.push('proxy');
        return keys;
    }

    // Get capability keys for a registry entry.
    // Prefers the entry's declared capabilities (which may include e.g. proxy
    // alongside ui), falling back to the capability implied by its type.
    function getRegistryCapabilityKeys(entry: RegistryEntry): string[] {
        if (entry.capabilities && Object.keys(entry.capabilities).length > 0) {
            return Object.keys(entry.capabilities).filter(k => !!CAPABILITY_DESCRIPTIONS[k]);
        }
        const capMap: Record<string, string> = { lsp: 'lsp', ui: 'ui', css: 'css', agent: 'agent' };
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
            registryEntries = await getRegistryIndex();
        } catch (e: any) {
            error = e.message || 'Failed to load registry';
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

    // Install from registry, streaming progress into the card as it happens.
    async function handleInstall(name: string) {
        if (installing.has(name)) return;
        error = '';
        installing = new Set(installing).add(name);
        try {
            for await (const event of installExtension(name)) {
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
            error = e.message || 'Failed to install extension';
        } finally {
            const next = new Set(installing);
            next.delete(name);
            installing = next;
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
            updates = await checkUpdates();
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
        try {
            for await (const event of updateExtensionImage(id)) {
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
            error = e.message || 'Failed to update extension';
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
        const lsp = ext.manifest.capabilities?.lsp;
        editLspSettings = {
            editor_enabled: lsp?.editor_enabled ?? true,
            tool_calls_enabled: lsp?.tool_calls_enabled ?? true,
        };
    }

    // Close edit form
    function closeEdit() {
        editingExtension = null;
        editExtensionId = null;
        editConfig = {};
        editLspSettings = { editor_enabled: true, tool_calls_enabled: true };
    }

    // Save edit
    async function handleSave() {
        if (!editExtensionId) return;
        saving = true;
        error = '';
        try {
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
            error = e.message || 'Failed to save extension';
        } finally {
            saving = false;
        }
    }

    // Get config schema fields for an extension
    function getConfigSchemaFields(ext: Extension): Array<{ key: string; title: string; type: string; description?: string; default?: any; sensitive?: boolean; enum?: any[] }> {
        const schema = ext.manifest.config_schema;
        if (!schema || !schema.properties) return [];
        return schema.properties.map((p: SchemaProperty) => ({
            key: p.key,
            title: p.title || p.key,
            description: p.description,
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
            await loadExtensions();
        })();
    });
</script>

<div class="extensions-tab">
    {#if error}
        <div class="error-banner" role="button" tabindex="0" onclick={() => error = ''} onkeydown={(e) => e.key === 'Enter' && (error = '')}>
            {error}
            <button class="dismiss" aria-label="Dismiss">&#x2715;</button>
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
                                    <div class="ext-name-row">
                                        <span class="ext-name">{ext.manifest.display_name || ext.name}</span>
                                        {#if getCapabilityKeys(ext).length > 0}
                                            <div class="capability-badges" role="group" aria-label="Capabilities">
                                                {#each getCapabilityKeys(ext) as capKey}
                                                    <button class="cap-badge" class:cap-lsp={capKey === 'lsp'} class:cap-ui={capKey === 'ui'} class:cap-css={capKey === 'css'} class:cap-agent={capKey === 'agent'} class:cap-proxy={capKey === 'proxy'} aria-label="{CAPABILITY_DESCRIPTIONS[capKey]?.label || capKey} capability for {ext.manifest.display_name || ext.name}. Click to learn more" onclick={() => { selectedExtension = ext; showCapabilitiesModal = true; }}>
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
                                        {#if ext.manifest.license_url} · <a href={safeUrl(ext.manifest.license_url)} target="_blank" rel="noopener noreferrer" class="ext-link">License</a>{/if}
                                        {#if ext.manifest.homepage} · <a href={safeUrl(ext.manifest.homepage)} target="_blank" rel="noopener noreferrer" class="ext-link">Website</a>{/if}
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
                            {#if buildProgress[ext.name]}
                                {@const bp = buildProgress[ext.name]}
                                <div class="ext-build" class:ext-build-error={!!bp.error}>
                                    <div class="ext-build-bar"><div class="ext-build-fill" style="--pct: {bp.progress}%"></div></div>
                                    <span class="ext-build-text">{bp.error || bp.message}</span>
                                </div>
                            {/if}
                            {#if ext.manifest.description}
                                <p class="ext-description">{ext.manifest.description}</p>
                            {/if}
                            {#if ext.manifest.capabilities?.proxy?.allowed_domains?.length}
                                <div class="ext-proxy-domains">
                                    <span class="ext-proxy-label">Connects to:</span>
                                    <div class="cap-detail-tags">
                                        {#each ext.manifest.capabilities.proxy.allowed_domains as domain}
                                            <span class="cap-detail-tag">{domain}</span>
                                        {/each}
                                    </div>
                                </div>
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
            <!-- LSP Settings -->
            {#if editingExtension?.type === 'lsp'}
                <div class="config-fields">
                    <h4>LSP Features</h4>
                    <div class="config-field">
                        <span class="field-label">Editor Features</span>
                        <p class="field-desc">Enable LSP in the editor: diagnostics, completions, go-to-definition, and hover.</p>
                        <ToggleSwitch
                            checked={editLspSettings.editor_enabled}
                            onChange={() => editLspSettings.editor_enabled = !editLspSettings.editor_enabled}
                            ariaLabel="Enable LSP features in editor"
                        />
                    </div>
                    <div class="config-field">
                        <span class="field-label">Tool Calls</span>
                        <p class="field-desc">Enable LSP tool support: references, definition, code actions, and hover.</p>
                        <ToggleSwitch
                            checked={editLspSettings.tool_calls_enabled}
                            onChange={() => editLspSettings.tool_calls_enabled = !editLspSettings.tool_calls_enabled}
                            ariaLabel="Enable LSP tool calls for agent"
                        />
                    </div>
                </div>
            {/if}
        </BaseModal>
    {/if}

    <!-- Capabilities Modal -->
    {#if showCapabilitiesModal && selectedExtension}
        {#snippet capsFooter()}
            <button type="button" class="btn-secondary" onclick={() => { showCapabilitiesModal = false; selectedExtension = null; }}>Close</button>
        {/snippet}

        <BaseModal title="Extension Capabilities" width="520px" footer={capsFooter} onClose={() => { showCapabilitiesModal = false; selectedExtension = null; }}>
            <div class="caps-modal-body">
                <div class="caps-ext-header">
                    <div class="caps-ext-icon" class:ext-lsp={selectedExtension.type === 'lsp'} class:ext-ui={selectedExtension.type === 'ui'} class:ext-css={selectedExtension.type === 'css'} class:ext-agent={selectedExtension.type === 'agent'} aria-hidden="true">
                        {selectedExtension.type === 'lsp' ? 'LSP' : selectedExtension.type === 'css' ? 'CSS' : selectedExtension.type === 'agent' ? 'AGENT' : 'UI'}
                    </div>
                    <div class="caps-ext-info">
                        <h3 class="caps-ext-name">{selectedExtension.manifest.display_name || selectedExtension.name}</h3>
                        {#if selectedExtension.manifest.description}
                            <p class="caps-ext-desc">{selectedExtension.manifest.description}</p>
                        {/if}
                    </div>
                </div>

                <div class="caps-list" role="list" aria-label="Capability details">
                    {#each getCapabilityKeys(selectedExtension) as capKey}
                        {@const info = CAPABILITY_DESCRIPTIONS[capKey]}
                        {@const caps = selectedExtension.manifest.capabilities}
                        {#if info}
                            <div class="cap-detail" role="listitem">
                                <div class="cap-detail-header">
                                    <span class="cap-detail-badge" class:cap-lsp={capKey === 'lsp'} class:cap-ui={capKey === 'ui'} class:cap-css={capKey === 'css'} class:cap-agent={capKey === 'agent'} class:cap-proxy={capKey === 'proxy'}>
                                        {info.label}
                                    </span>
                                </div>
                                <p class="cap-detail-desc">{info.description}</p>
                                <!-- Proxy: show allowed domains -->
                                {#if capKey === 'proxy' && caps?.proxy?.allowed_domains?.length}
                                    <div class="cap-detail-extra">
                                        <span class="cap-detail-label">Allowed domains:</span>
                                        <div class="cap-detail-tags">
                                            {#each caps.proxy.allowed_domains as domain}
                                                <span class="cap-detail-tag">{domain}</span>
                                            {/each}
                                        </div>
                                    </div>
                                {/if}
                                <!-- UI: show injection points -->
                                {#if capKey === 'ui' && caps?.ui?.injection_points?.length}
                                    <div class="cap-detail-extra">
                                        <span class="cap-detail-label">Modifies:</span>
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
                                        <span class="cap-detail-label">Permissions:</span>
                                        <div class="cap-permission-list">
                                            {#each caps.ui.permissions as perm}
                                                {@const info = PERMISSION_DESCRIPTIONS[perm]}
                                                <div class="cap-permission">
                                                    <span class="cap-permission-name">{info?.label || perm}</span>
                                                    {#if info?.description}
                                                        <span class="cap-permission-desc">{info.description}</span>
                                                    {:else}
                                                        <span class="cap-permission-desc">Requested a permission this version does not document.</span>
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
                                            <span class="cap-detail-label">Language:</span>
                                            <span class="cap-detail-tags">
                                                <span class="cap-detail-tag">{caps.lsp.language}</span>
                                            </span>
                                        {/if}
                                        {#if caps.lsp.extensions?.length}
                                            <span class="cap-detail-label">File extensions:</span>
                                            <div class="cap-detail-tags">
                                                {#each caps.lsp.extensions as ext}
                                                    <span class="cap-detail-tag">.{ext}</span>
                                                {/each}
                                            </div>
                                        {/if}
                                    </div>
                                {/if}
                                <!-- CSS: show stylesheet URL -->
                                {#if capKey === 'css' && caps?.css?.css_url}
                                    <div class="cap-detail-extra">
                                        <span class="cap-detail-label">Stylesheet:</span>
                                        <code class="cap-detail-value">{caps.css.css_url}</code>
                                    </div>
                                {/if}
                            </div>
                        {/if}
                    {/each}
                </div>
            </div>
        </BaseModal>
    {/if}

    <!-- Browse View -->
    {#if activeView === 'browse'}
        <div class="browse-view">

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
                        {@const regCapKeys = getRegistryCapabilityKeys(entry)}
                        <div class="extension-card registry-card">
                            <div class="card-header">
                                <div class="ext-icon" class:ext-lsp={entry.type === 'lsp'} class:ext-ui={entry.type === 'ui'} class:ext-css={entry.type === 'css'} class:ext-agent={entry.type === 'agent'}>
                                    {entry.type === 'lsp' ? 'LSP' : entry.type === 'css' ? 'CSS' : entry.type === 'agent' ? 'AGENT' : 'UI'}
                                </div>
                                <div class="ext-info">
                                    <div class="ext-name-row">
                                        <span class="ext-name">{entry.display_name || entry.name}</span>
                                        {#if regCapKeys.length > 0}
                                            <div class="capability-badges" role="group" aria-label="Capabilities">
                                                {#each regCapKeys as regCapKey}
                                                    {@const regCapInfo = CAPABILITY_DESCRIPTIONS[regCapKey]}
                                                    <button class="cap-badge" class:cap-lsp={regCapKey === 'lsp'} class:cap-ui={regCapKey === 'ui'} class:cap-css={regCapKey === 'css'} class:cap-agent={regCapKey === 'agent'} class:cap-proxy={regCapKey === 'proxy'} aria-label="{regCapInfo.label} capability for {entry.display_name || entry.name}. {regCapInfo.description}" onclick={() => { selectedExtension = { name: entry.name, manifest: { display_name: entry.display_name, description: entry.description, capabilities: getRegistryCapabilities(entry) }, type: entry.type } as Extension; showCapabilitiesModal = true; }}>
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
                                        {#if entry.license_url} · <a href={safeUrl(entry.license_url)} target="_blank" rel="noopener noreferrer" class="ext-link">License</a>{/if}
                                        {#if entry.homepage} · <a href={safeUrl(entry.homepage)} target="_blank" rel="noopener noreferrer" class="ext-link">Website</a>{/if}
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
                            {#if buildProgress[entry.name]}
                                {@const bp = buildProgress[entry.name]}
                                <div class="ext-build" class:ext-build-error={!!bp.error}>
                                    <div class="ext-build-bar"><div class="ext-build-fill" style="--pct: {bp.progress}%"></div></div>
                                    <span class="ext-build-text">{bp.error || bp.message}</span>
                                </div>
                            {/if}
                            {#if entry.description}
                                <p class="ext-description">{entry.description}</p>
                            {/if}
                            {#if entry.capabilities?.proxy?.allowed_domains?.length}
                                <div class="ext-proxy-domains">
                                    <span class="ext-proxy-label">Connects to:</span>
                                    <div class="cap-detail-tags">
                                        {#each entry.capabilities.proxy.allowed_domains as domain}
                                            <span class="cap-detail-tag">{domain}</span>
                                        {/each}
                                    </div>
                                </div>
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
        filter: brightness(1.2);
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

    .cap-proxy {
        background: rgba(236, 72, 153, 0.15);
        color: #ec4899;
        border-color: rgba(236, 72, 153, 0.3);
    }

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
        font-size: 0.8rem;
        font-weight: 600;
        color: var(--color-text-primary);
    }

    .cap-permission-desc {
        font-size: 0.78rem;
        color: var(--color-text-secondary);
        line-height: 1.4;
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
