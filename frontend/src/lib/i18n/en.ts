/**
 * `en.ts` — the English catalog of record.
 *
 * One flat object of dotted keys → message values. Every i18n batch appends **its own
 * section** to this file, delimited by a `═══ namespace ═══` banner, so the boundary
 * between batches stays obvious and two batches cannot overwrite each other.
 *
 * House rules (they are what keeps a translation honest, not decoration):
 *  - A value is either a `string` with **named** `{placeholders}` — never positional —
 *    or a plural table `{ one, other, … }` keyed by `Intl.PluralRules` categories,
 *    selected from a `count` param. `other` is required: every locale has it.
 *  - English values are the copy that shipped in the components, byte-identical. A
 *    translated catalog changes nothing here.
 *  - **Never** a key for text this app does not own: extension `display_name`,
 *    `description`, `config_schema` titles/descriptions, changelog entries, log lines,
 *    or anything a manifest or an API response supplies at runtime. That text is the
 *    extension's, and translating it silently would put words in its author's mouth.
 */

/** The categories `Intl.PluralRules` can answer with. */
export type PluralCategory = 'zero' | 'one' | 'two' | 'few' | 'many' | 'other';

/** A plural message: at least `other`, plus whatever categories the language needs. */
export type PluralTable = Partial<Record<PluralCategory, string>> & { other: string };

export type MessageValue = string | PluralTable;

/** Interpolation params. `count` additionally selects the plural form. */
export type MessageParams = Record<string, string | number | boolean | null | undefined>;

export const en = {
    // ════════════════════════════════════════════════════════════════════════════
    // ═══ namespace: settings.* — Settings / ConfigModal / Extensions batch      ═══
    // ═══ (ConfigModal.svelte, ConfigModal/*.svelte, StatsModal.svelte,          ═══
    // ═══  utils/providers.ts)                                                    ═══
    // ════════════════════════════════════════════════════════════════════════════

    // ── i18n runtime (self-test surface; replaced when T5's catalog lands) ──
    'i18n.test.plain': 'Plain message',
    'i18n.test.files': { one: '{count} file', other: '{count} files' },

    // ── ConfigModal.svelte — shell, tabs, provider pairs, About, footer ──
    'settings.title': 'Settings',
    'settings.close': 'Close',
    'settings.toggleFullscreen': 'Toggle fullscreen',
    'settings.tab.providers': 'Providers',
    'settings.tab.taskDefaults': 'Task Defaults',
    'settings.tab.preferences': 'Preferences',
    'settings.tab.extensions': 'Extensions',
    'settings.tab.about': 'About',
    'settings.providers': 'Providers',
    'settings.primaryProviders': 'Primary Providers',
    'settings.primaryProvidersHelp': 'Select default provider/model for each task type.',
    'settings.pair.worker': 'Worker',
    'settings.pair.workerHint': 'Model used for chat responses',
    'settings.pair.utility': 'Utility',
    'settings.pair.utilityHint': 'Model for summaries, commit messages, labels',
    'settings.pair.providerAria': '{title} provider',
    'settings.pair.modelAria': '{title} model',
    'settings.selectProvider': 'Select provider',
    'settings.selectModel': 'Select model',
    'settings.loadingModels': 'Loading models...',
    'settings.addProvider': '+ Add Provider',
    'settings.addProviderHint': 'Add a new LLM provider',
    'settings.noLlmWarning.before': 'No LLM provider configured. An LLM provider is required for',
    'settings.noLlmWarning.labels': 'commit label generation',
    'settings.noLlmWarning.between': 'and',
    'settings.noLlmWarning.resolution': 'automatic merge conflict resolution',
    'settings.noProviders': 'No providers configured',
    'settings.providersEmptyHint': 'Add an LLM provider to get started. Supports any OpenAI-compatible API.',
    'settings.validateWorker': 'Please select a Worker provider and model.',
    'settings.validateUtility': 'Please select a Utility provider and model.',
    'settings.agentConnectionTest': 'Agent provider — binary checked at runtime',
    'settings.discardTitle': 'Discard changes?',
    'settings.discardMessage': 'You have unsaved changes. Discard them?',
    'settings.discard': 'Discard',
    'settings.keepEditing': 'Keep editing',
    'settings.signOut': 'Sign out',
    'settings.signOutConfirm': 'Are you sure you want to sign out?',
    'settings.signOutHint': 'Sign out of Kanban',
    'settings.cancel': 'Cancel',
    'settings.cancelHint': 'Cancel and discard changes',
    'settings.save': 'Save',
    'settings.saving': 'Saving...',
    'settings.saveHint': 'Save settings',
    'settings.aboutTitle': 'MIKE: Integrated Kanban Environment',
    'settings.pwa': 'Progress Web App',
    'settings.pwaInstall': 'Install as PWA',
    'settings.pwaInstallHint': 'Install as a Progressive Web App',
    'settings.systemInfo': 'System Info',
    'settings.worktreesDir': 'Directory for Worktrees',
    'settings.worktreesDirHelp': 'Directory where task worktrees are created',
    'settings.database': 'Database',
    'settings.databaseHelp': "Path to the server's SQLite database",
    'settings.caCert': 'CA Certificate',
    'settings.caCertDownload': 'Download CA Certificate',
    'settings.caCertHint': "Download the CA certificate that signed this server's certificate",
    'settings.caCertHelp': "This server's certificate is signed by its own certificate authority. Import this file into another device's trust store to trust the connection there.",

    // ── ConfigModal/PreferencesTab.svelte — shortcut rows + UI preferences ──
    // Only labels and hints are copy: the row `id`s are the `keyboard_shortcuts` field names
    // and `formatShortcut()` output is a binding, so the save payload never sees a translation.
    'settings.preferences.keyboard': 'Keyboard Shortcuts',
    'settings.preferences.hideHints': 'Hide shortcut hints',
    'settings.preferences.hideHintsDesc': 'Hide the keycaps shown on buttons',
    'settings.preferences.shortcut.createTask': 'Create Task',
    'settings.preferences.shortcut.createTaskDesc': 'Opens the create-task dialog on the project board',
    'settings.preferences.shortcut.createTaskSubmit': 'Create Task (submit)',
    'settings.preferences.shortcut.createTaskSubmitDesc': 'Creates the task inside the create-task dialog',
    'settings.preferences.shortcut.openTerminal': 'Open Terminal',
    'settings.preferences.shortcut.openTerminalDesc': 'Opens the terminal panel on pages with a terminal button',
    'settings.preferences.shortcut.startTask': 'Start Task',
    'settings.preferences.shortcut.startTaskDesc': 'Starts the task in the task view while the Start Task button is shown',
    'settings.preferences.shortcut.toggleFullscreen': 'Toggle Fullscreen',
    'settings.preferences.shortcut.toggleFullscreenDesc': 'Maximizes or minimizes the focused area (chat, diff, editor, terminal)',
    'settings.preferences.shortcut.multiSelect': 'Multi-Select Cards',
    'settings.preferences.shortcut.multiSelectDesc': 'Modifier+Click to select multiple task cards',
    'settings.preferences.resetToDefault': 'Reset to default',
    'settings.preferences.resetToDefaultWith': 'Reset to default ({shortcut})',
    'settings.preferences.reset': 'Reset',
    'settings.preferences.clearShortcut': 'Clear shortcut',
    'settings.preferences.disableShortcut': 'Disable this shortcut',
    'settings.preferences.clear': 'Clear',
    'settings.preferences.shortcutAria': '{name} shortcut: {binding}. Click to reassign.',
    'settings.preferences.capturing': 'capturing',
    'settings.preferences.disabled': 'disabled',
    'settings.preferences.clickHint': 'Click anywhere with a modifier held (Escape to cancel)',
    'settings.preferences.keyHint': 'Press any key (Escape to cancel)',
    'settings.preferences.clickToReassign': 'Click to reassign',
    'settings.preferences.nowClick': 'Now modifier+click...',
    'settings.preferences.pressKey': 'Press a key...',
    'settings.preferences.none': 'None',
    'settings.preferences.invalidClick': 'Invalid combo — hold Ctrl, Cmd, Shift or Alt and click.',
    'settings.preferences.invalidKey': 'Invalid key — use a letter or a modifier combo.',
    'settings.preferences.ui': 'UI',
    'settings.preferences.hideModelBadge': 'Hide model badge',
    'settings.preferences.hideModelBadgeDesc': 'Hide the model badge shown on task cards',
    'settings.preferences.oldestFirst': 'Oldest Task on top',
    'settings.preferences.oldestFirstDesc': 'Put the least recently moved task at the top of each column',

    // ── ConfigModal/TaskDefaults.svelte ──
    // `autoMergeDesc` is split around the inline branch badge; the two halves carry the
    // surrounding spaces exactly as the markup did, so the rendered sentence is unchanged.
    'settings.defaults.description': 'These settings apply to newly created tasks',
    'settings.defaults.operatingMode': 'Operating mode',
    'settings.defaults.autoMerge': 'Auto-merge',
    'settings.defaults.autoMergeAria': 'Auto-merge into main',
    'settings.defaults.autoMergeDesc.before': 'Merge changes into',
    'settings.defaults.autoMergeDesc.after': 'when complete',
    'settings.defaults.branchTitle': 'Branch: {branch}',
    'settings.defaults.subtasksAutoMerge': 'Allow subtasks to auto-merge',
    'settings.defaults.subtasksAutoMergeDesc': 'Subtasks created under a task will auto-merge into it',
    'settings.defaults.autoSummarize': 'Auto-summarize threshold',
    'settings.defaults.autoSummarizeDesc': "Summarize context when it reaches this % of the model's context window",
    'settings.defaults.runParallel': 'Run siblings in parallel',
    'settings.defaults.runParallelDesc': "Start all of a parent's subtasks at once instead of one at a time",
    'settings.defaults.maxFailed': 'Max failed turns',
    'settings.defaults.maxFailedDesc': 'Stop task after this many consecutive LLM failures',

    // ── ConfigModal/AgentSection.svelte — the manifest's own description/setup_info is
    //    extension text and is never a key; only the chrome around it is. ──
    'settings.agent.setup': 'Setup',
    'settings.agent.managedBefore': 'This provider is supplied by the',
    'settings.agent.managedAfter': 'extension. To remove it, disable or uninstall the extension in Settings → Extensions.',
    'settings.agent.notInstalled': 'Extension is not installed',

    // ── ConfigModal/PricingSection.svelte + HelpIcon.svelte ──
    'settings.pricing.title': 'Pricing Defaults (USD per 1M tokens)',
    'settings.pricing.help': "Provider-wide defaults applied to every model that doesn't set its own pricing. Set per-model prices by expanding a model row above. If Cache Write is set, it replaces Input for non-cached tokens.",
    'settings.pricing.input': 'Input',
    'settings.pricing.output': 'Output',
    'settings.pricing.cacheRead': 'Cache Read',
    'settings.pricing.cacheWrite': 'Cache Write',
    'settings.pricing.powerTitle': 'Power & Electricity Defaults',
    'settings.pricing.powerHelp': 'Provider-wide defaults. Power (W) × generation time gives kWh; combined with the electricity rate, adds to total cost. A model can override power via its own row; the electricity rate always applies.',
    'settings.pricing.power': 'Power Consumption (W)',
    'settings.pricing.electricity': 'Electricity ({currency}/kWh)',
    'settings.help.aria': '{label} help',

    'settings.models.title': 'MODELS',
    'settings.models.addModelHint': 'Add a model manually',
    'settings.models.addModel': 'Add Model',
    'settings.models.colModel': 'Model',
    'settings.models.colContext': 'Context Window',
    'settings.models.colEffort': 'Reasoning Effort',
    'settings.models.nameField': 'Model name',
    'settings.models.ctxHint': 'Context window in tokens. Pre-populated from Fetch Models; edit to override, or leave empty for auto-discovery.',
    'settings.models.auto': 'auto',
    'settings.models.thisModel': 'this model',
    'settings.models.modelWord': 'model',
    'settings.models.ctxAria': 'Context window for {model}',
    'settings.models.effortAria': 'Reasoning effort for {model}',
    'settings.models.effortHint': 'Reasoning effort (thinking budget). Pick a known level or type a custom value; custom values are sent to the server as-is.',
    'settings.models.collapsePricingAria': 'Collapse pricing for {model}',
    'settings.models.expandPricingAria': 'Expand pricing for {model}',
    'settings.models.collapsePricingTitle': 'Collapse model pricing & power',
    'settings.models.expandPricingTitle': 'Model pricing & power',
    'settings.models.removeAria': 'Remove {model}',
    'settings.models.detailsTitle': 'Model Pricing & Power',
    'settings.models.detailsHint': 'Leave empty to use the provider defaults below.',
    'settings.models.power': 'Power (W)',
    'settings.models.powerHint': 'Power draw override in watts. Empty = use provider default. Used for electricity cost estimates.',
    'settings.models.note': 'Prices are USD per 1M tokens. Cache Write replaces Input for non-cached tokens when set.',
    'settings.models.empty': 'No models added. Use "Fetch Models" or add manually.',

    // ── ConfigModal/SessionsSection.svelte ──
    // The `refresh-sessions` / `revoke-all-other-sessions` / `revoke-session-<id>` aria-labels
    // are identifiers the e2e suite selects on, not prose — they stay out of the catalog.
    // Destructive copy is deliberately literal, one key per decision the user makes.
    'settings.sessions.title': 'Sessions',
    'settings.sessions.reloadTitle': 'Reload the session list',
    'settings.sessions.loading': 'Loading...',
    'settings.sessions.refresh': 'Refresh',
    'settings.sessions.revokeAllHint': 'Sign out every other device',
    'settings.sessions.revokeAllOther': 'Revoke all other sessions',
    'settings.sessions.revoking': 'Revoking...',
    'settings.sessions.revoke': 'Revoke',
    'settings.sessions.revokeAll': 'Revoke all',
    'settings.sessions.loadingList': 'Loading sessions...',
    'settings.sessions.none': 'No other active sessions',
    'settings.sessions.unknownDevice': 'Unknown device',
    'settings.sessions.unknownTime': 'Unknown',
    'settings.sessions.thisDevice': 'This device',
    'settings.sessions.created': 'Created',
    'settings.sessions.lastSeen': 'Last seen',
    'settings.sessions.ip': 'IP',
    'settings.sessions.signedInAt': 'Signed in at {time}',
    'settings.sessions.lastSeenAt': 'Last seen at {time}',
    'settings.sessions.revokeDeviceHint': 'Revoke this device and sign out',
    'settings.sessions.revokeSessionHint': 'Sign out this session',
    'settings.sessions.revokeThisDevice': 'Revoke this device?',
    'settings.sessions.revokeThisSession': 'Revoke this session?',
    'settings.sessions.revokeThisDeviceMessage': 'You are signed in on this device. Revoking it signs you out now.',
    'settings.sessions.revokeOtherMessage': 'The other device will be signed out and must sign in again.',
    'settings.sessions.revokeAllTitle': 'Revoke all other sessions?',
    'settings.sessions.revokeAllMessage': {
        one: '{count} other device will be signed out and must sign in again. This device stays signed in.',
        other: '{count} other devices will be signed out and must sign in again. This device stays signed in.',
    },
    'settings.sessions.revokedToast': 'Session revoked',
    'settings.sessions.revokedCountToast': {
        one: '{count} other session revoked',
        other: '{count} sessions revoked',
    },
    'settings.sessions.loadFailed': 'Failed to load sessions',
    'settings.sessions.revokeFailed': 'Failed to revoke session',
    'settings.sessions.revokeAllFailed': 'Failed to revoke other sessions',

    // ── ConfigModal/ProviderCard.svelte (+ the two display fallbacks in utils/providers.ts) ──
    // `fetchError` / `testResult.message` are server answers and stay untranslated.
    'settings.provider.removeTitle': 'Remove Provider',
    'settings.provider.removeMessage': 'Remove this provider? This cannot be undone.',
    'settings.provider.remove': 'Remove',
    'settings.provider.toggleAria': 'Toggle {name} settings',
    'settings.provider.newProvider': 'New Provider',
    'settings.provider.dragHint': 'Drag to reorder',
    'settings.provider.dragAria': 'Drag to reorder provider',
    'settings.provider.agent': 'Agent',
    'settings.provider.agentFallback': 'Agent',
    'settings.provider.providerFallback': 'Provider',
    'settings.provider.removeHint': 'Remove provider',
    'settings.provider.collapseAria': 'Collapse provider details',
    'settings.provider.expandAria': 'Expand provider details',
    'settings.provider.collapse': 'Collapse',
    'settings.provider.expand': 'Expand',
    'settings.provider.url': 'Provider URL',
    'settings.provider.apiKey': 'API key',
    'settings.provider.optional': '(optional)',
    'settings.provider.apiKeyPlaceholder': 'API key...',
    'settings.provider.fetchHint': 'Fetch available models from this provider',
    'settings.provider.fetching': 'Fetching...',
    'settings.provider.fetchModels': 'Fetch Models',
    'settings.provider.testHint': 'Test connection to this provider',
    'settings.provider.testing': 'Testing...',
    'settings.provider.testConnection': 'Test Connection',
    'settings.provider.concurrency': 'Concurrency',
    'settings.provider.concurrencyHelp': 'Max tasks running concurrently on this provider; also caps in-flight LLM requests to this number.',
    'settings.provider.maxRps': 'Max requests/sec',
    'settings.provider.maxRpsHelp': 'Maximum new requests started per second to this provider. 0 = unlimited.',

    // ── StatsModal.svelte — token usage table. `t/s`, `kWh`, `Wh`, `mWh` are units, not copy. ──
    'settings.stats.title': 'Token Usage',
    'settings.stats.loading': 'Loading stats...',
    'settings.stats.empty': 'No token usage data yet. Run a task to see stats here.',
    'settings.stats.tasks': 'Tasks',
    'settings.stats.cached': 'Cached',
    'settings.stats.prompt': 'Prompt',
    'settings.stats.generated': 'Generated',
    'settings.stats.promptSpeed': 'Prompt Speed',
    'settings.stats.genSpeed': 'Gen Speed',
    'settings.stats.power': 'Power',
    'settings.stats.estCost': 'Est. Total Cost',
    'settings.stats.costHint': 'Includes API pricing and electricity cost (when configured)',
    'settings.stats.model': 'Model',
    'settings.stats.resetHint': 'Reset stats for this model',
    'settings.stats.resetAria': 'Reset stats for {model}',
    'settings.stats.resetConfirm': 'Reset token stats for {model}?',
    'settings.stats.reset': 'Reset',

    // ── ConfigModal/ExtensionsTab.svelte — the three label dictionaries ──
    // Injection points, capabilities and permission explanations. The **permission ids
    // themselves** (ui.modal, editor.save, …) stay in the component as data — they are the
    // names the manifest declares and the host enforces; only their explanations are copy.
    // The `i18n` capability below is new copy from the i18n-extension-type task.
    'settings.extensions.point.taskCard': 'Task Cards',
    'settings.extensions.point.fileEditor': 'File Editor',
    'settings.extensions.point.projectBoard': 'Project Board',
    'settings.extensions.point.taskChat': 'Task Chat',
    'settings.extensions.point.projectSidebar': 'Project Sidebar',
    'settings.extensions.point.fileBrowser': 'File Browser',
    'settings.extensions.point.createTask': 'Create Task',
    'settings.extensions.cap.network.label': 'Network',
    'settings.extensions.cap.network.description': 'Enables external API access for specific domains, within the boundaries of declared capabilities.',
    'settings.extensions.cap.lsp.label': 'LSP',
    'settings.extensions.cap.lsp.description': 'Provides a Language Server Protocol server for code intelligence — completions, diagnostics, go-to-definition, and more. Runs in an isolated gVisor sandbox.',
    'settings.extensions.cap.ui.label': 'UI',
    'settings.extensions.cap.ui.description': 'Customizes interactive elements. The component runs within a sandboxed iframe.',
    'settings.extensions.cap.css.label': 'CSS',
    'settings.extensions.cap.css.description': 'Loads a global stylesheet that applies custom styling across the entire application. Used for themes and visual customizations.',
    'settings.extensions.cap.agent.label': 'Agent',
    'settings.extensions.cap.agent.description': 'Adds an AI coding agent as a provider option.',
    'settings.extensions.cap.llm.label': 'LLM',
    'settings.extensions.cap.llm.description': 'Modifies the OpenAI-compatible LLM request format to fit provider-specific structure, depending on the provider URL.',
    'settings.extensions.perm.createTaskDescriptionInsertText.label': 'Edit the task description',
    'settings.extensions.perm.createTaskDescriptionInsertText.description': 'Can insert text into the task description field.',
    'settings.extensions.perm.createTaskEventsPaste.label': 'See what you paste',
    'settings.extensions.perm.createTaskEventsPaste.description': 'Can see text pasted into the task description so it can act on it.',
    'settings.extensions.perm.networkProxy.label': 'Reach external services',
    'settings.extensions.perm.networkProxy.description': 'Can make requests to external services through the server, limited to the allowed domains of its Network capability.',
    'settings.extensions.perm.uiSetSize.label': 'Resize its own panel',
    'settings.extensions.perm.uiSetSize.description': 'Can change the height of the space it occupies on the page.',
    'settings.extensions.perm.uiModal.label': 'Show a modal',
    'settings.extensions.perm.uiModal.description': 'Can display a popup modal.',
    'settings.extensions.perm.storageGet.label': 'Read its saved data',
    'settings.extensions.perm.storageGet.description': 'Can read data it previously saved for the current task or project.',
    'settings.extensions.perm.storageSet.label': 'Save its own data',
    'settings.extensions.perm.storageSet.description': 'Can save small amounts of data for the current task or project.',
    'settings.extensions.perm.storageDelete.label': 'Delete its saved data',
    'settings.extensions.perm.storageDelete.description': 'Can remove data it previously saved.',
    'settings.extensions.perm.storageList.label': 'List its saved data',
    'settings.extensions.perm.storageList.description': 'Can list the data it saved for the current task or project.',
    'settings.extensions.perm.editorAddFooterButton.label': 'Add a button to the editor',
    'settings.extensions.perm.editorAddFooterButton.description': 'Can add a button to the bottom toolbar of the code editor.',
    'settings.extensions.perm.editorGetContent.label': 'Read the open file',
    'settings.extensions.perm.editorGetContent.description': 'Can read the full contents of the file currently open in the editor, and keep seeing it as the file is edited.',
    'settings.extensions.perm.editorSetContent.label': 'Write to the open file',
    'settings.extensions.perm.editorSetContent.description': 'Can write the full contents of the file currently open in the editor.',
    'settings.extensions.perm.editorSetOverlay.label': 'Replace the editor view',
    'settings.extensions.perm.editorSetOverlay.description': 'Can hide the code editor, show its own view in its place, and restore the editor.',
    'settings.extensions.perm.editorJumpToLine.label': 'Jump to a line in the file',
    'settings.extensions.perm.editorJumpToLine.description': 'Can scroll the editor to a specific line.',
    'settings.extensions.perm.editorSave.label': 'Save the open file',
    'settings.extensions.perm.editorSave.description': 'Can save the file currently open in the editor.',
    'settings.extensions.perm.previewServeScripts.label': 'Run scripts in the preview',
    'settings.extensions.perm.previewServeScripts.description': 'Can run scripts in the preview',
    'settings.extensions.perm.previewServeNetwork.label': 'Send/receive data in the preview',
    'settings.extensions.perm.previewServeNetwork.description': 'Can use commands such as fetch() to send/receive data across the network.',
    'settings.extensions.cap.i18n.label': 'I18N',
    'settings.extensions.cap.i18n.description': 'Installs translated interface text and agent prompt templates for one language',

    // ── ConfigModal/ExtensionsTab.svelte — chrome, dialogs, browse + installed cards ──
    // Build progress, install/update streams and `environment.note` are server- and
    // manifest-supplied text and stay out of the catalog. The type badges (`LSP`, `CSS`,
    // `AGENT`, `LLM`, `I18N`, `UI`) are acronyms, rendered identically to the five that
    // already shipped as literals. `localeHint` labels the pack's own declared locale.
    'settings.extensions.loadFailed': 'Failed to load extensions',
    'settings.extensions.registryLoadFailed': 'Failed to load registry',
    'settings.extensions.installFailed': 'Failed to install extension',
    'settings.extensions.deleteFailed': 'Failed to delete extension',
    'settings.extensions.updateFailed': 'Failed to update extension',
    'settings.extensions.checkUpdatesFailed': 'Failed to check for updates',
    'settings.extensions.saveFailed': 'Failed to save extension',
    'settings.extensions.depRejected': 'That directory cannot be used',
    'settings.extensions.deleteTitle': 'Delete "{name}"?',
    'settings.extensions.deleteMessage': 'This will stop its sandbox and delete the extension, including its files under the extensions directory. This cannot be undone.',
    'settings.extensions.delete': 'Delete',
    'settings.extensions.deleteLeftoverError': 'Deleted, but its files could not be removed and are still on disk: {files}',
    'settings.extensions.updateImageTitle': 'Update Image',
    'settings.extensions.updateImageMessage': 'Fetch the latest image for {name}? The sandbox will be restarted.',
    'settings.extensions.update': 'Update',
    'settings.extensions.notBuilt': 'Not built',
    'settings.extensions.builtPrefix': 'Built ',
    'settings.extensions.builtAt': 'Built {date}',
    'settings.extensions.dismiss': 'Dismiss',
    'settings.extensions.installedCount': 'Installed ({count})',
    'settings.extensions.browse': 'Browse',
    'settings.extensions.checking': 'Checking...',
    'settings.extensions.checkUpdates': 'Check for Updates',
    'settings.extensions.searchPlaceholder': 'Search extensions...',
    'settings.extensions.noUpdates': 'No Updates Available',
    'settings.extensions.upToDate': 'All installed extensions are up to date.',
    'settings.extensions.updatesAvailable': 'Updates Available',
    'settings.extensions.updating': 'Updating...',
    'settings.extensions.updateAll': 'Update All',
    'settings.extensions.changesSince': 'Changes since {version}:',
    'settings.extensions.newPermsTitle': 'This update requests new permissions:',
    'settings.extensions.permDanger': 'Potentially dangerous: can escape the sandbox',
    'settings.extensions.permUndocumented': 'Requested a permission this version does not document.',
    'settings.extensions.loadingExtensions': 'Loading extensions...',
    'settings.extensions.noneInstalled': 'No extensions installed',
    'settings.extensions.noneInstalledHelp': 'Install extensions from the registry to add LSP servers and UI plugins.',
    'settings.extensions.localInstalledTitle': 'Installed from the local extensions directory. Click to show its path.',
    'settings.extensions.localUnpublishedTitle': 'Present in the local extensions directory but not published in the registry. Click to show its path.',
    'settings.extensions.local': 'Local',
    'settings.extensions.featured': 'Featured',
    'settings.extensions.needsSetupTitle': 'Its dependencies directory is not configured — click to choose one',
    'settings.extensions.needsSetupRegistryTitle': 'Needs setup after installation — it reads resolved dependencies from {env}={container} inside its sandbox',
    'settings.extensions.needsSetup': 'Needs setup',
    'settings.extensions.capabilitiesGroup': 'Capabilities',
    'settings.extensions.capAria': '{label} capability for {name}. Click to learn more',
    'settings.extensions.capAriaFull': '{label} capability for {name}. {description}',
    'settings.extensions.license': 'License',
    'settings.extensions.website': 'Website',
    'settings.extensions.localeHint': 'The language this pack translates',
    'settings.extensions.editNeedsSetup': 'Edit — its dependencies directory is not configured',
    'settings.extensions.edit': 'Edit',
    'settings.extensions.envNote': 'This language server cannot resolve the project it is serving, so its unresolved-import diagnostics are being withheld.',
    'settings.extensions.path': 'Path',
    'settings.extensions.pathUnavailable': 'Path unavailable',
    'settings.extensions.connectsTo': 'Connects to:',
    'settings.extensions.agentNote.before': 'This will add',
    'settings.extensions.agentNote.after': 'as an option in',
    'settings.extensions.editTitle': 'Edit: {name}',
    'settings.extensions.dependencies': 'Dependencies',
    'settings.extensions.depDirLabel': 'Dependencies directory',
    'settings.extensions.depDirDesc.before': 'This LSP reads resolved dependencies from',
    'settings.extensions.depDirDesc.after': "inside its sandbox. To allow tasks to update dependencies, the project's Sandbox Configuration should be updated with the same folder mounted with read-write permissions.",
    'settings.extensions.depAria': 'Host directory holding {name} resolved dependencies',
    'settings.extensions.depMounted': 'Mounted Read-only',
    'settings.extensions.depMountedTitle': '{host} → {container} in this server’s sandbox, read-only',
    'settings.extensions.depUnmountHint': 'Unmount the folder when you click save',
    'settings.extensions.depUnmount': 'Unmount',
    'settings.extensions.depNone': 'Nothing mounted yet.',
    'settings.extensions.depPending': '{action} when you save',
    'settings.extensions.depPendingUnmount': 'Unmounts this folder',
    'settings.extensions.depPendingReplace': 'Replaces the host directory',
    'settings.extensions.depPendingMount': 'Mounts this directory',
    'settings.extensions.configuration': 'Configuration',
    'settings.extensions.remove': 'Remove',
    'settings.extensions.addMount': 'Add mount',
    'settings.extensions.addVariable': 'Add variable',
    'settings.extensions.noLimit': 'No limit',
    'settings.extensions.noSchema': 'No configurable fields for this extension.',
    'settings.extensions.sandbox': 'Sandbox',
    'settings.extensions.memoryLimit': 'Memory Limit (MB)',
    'settings.extensions.memoryLimitDesc': 'Maximum memory for the language server sandbox. Blank = no limit. Applied immediately on save.',
    'settings.extensions.lspFeatures': 'LSP Features',
    'settings.extensions.editorFeatures': 'Editor Features',
    'settings.extensions.editorFeaturesDesc': 'Enable LSP in the editor: diagnostics, completions, go-to-definition, and hover.',
    'settings.extensions.editorFeaturesAria': 'Enable LSP features in editor',
    'settings.extensions.toolCalls': 'Tool Calls',
    'settings.extensions.toolCallsDesc': 'Enable LSP tool support: references, definition, rename, and hover.',
    'settings.extensions.toolCallsAria': 'Enable LSP tool calls for agent',
    'settings.extensions.capsTitle': 'Extension Capabilities',
    'settings.extensions.capsDetailsAria': 'Capability details',
    'settings.extensions.allowedDomains': 'Allowed domains:',
    'settings.extensions.modifies': 'Modifies:',
    'settings.extensions.permissions': 'Permissions:',
    'settings.extensions.language': 'Language:',
    'settings.extensions.fileTypes': 'File types:',
    'settings.extensions.stylesheet': 'Stylesheet:',
    'settings.extensions.installIntro': 'This extension requests the following permissions:',
    'settings.extensions.installTitle': 'Install "{name}"?',
    'settings.extensions.install': 'Install',
    'settings.extensions.installed': 'Installed',
    'settings.extensions.installingNow': 'Installing...',
    'settings.extensions.depPermission': 'A dependencies folder on this machine',
    'settings.extensions.depPermissionDesc.before': 'This language server reads already-downloaded dependencies from',
    'settings.extensions.depPermissionDesc.after': 'inside its sandbox. Installing it grants nothing by itself: afterwards you can point it at a folder of your choosing, and until you do it resolves only what its own image contains. That folder is then read by this extension and mounted nowhere else — you fill it, on the host or from a project whose own mounts name it.',
    'settings.extensions.registryEmpty': 'No extensions in registry',
    'settings.extensions.registryEmptyHelp': 'Check the registry URL or try a different source.',
    'settings.extensions.loadingRegistry': 'Loading registry...',
    'settings.extensions.needsSetupNote.before': 'Requires additional setup after installation: it reads resolved dependencies from',
    'settings.extensions.needsSetupNote.after': 'inside its sandbox. Point it at a folder of your choosing afterwards — until you do, it resolves only what its own image contains.',

    // ════════════════════════════════════════════════════════════════════════════
    // ═══ namespace: chat.* — chat surface batch                                 ═══
    // ═══ (components/Chat/*.svelte, ChangeModelModal.svelte, ModeSelect.svelte,  ═══
    // ═══  services/stream-handler.ts, utils/chat-entries.ts, utils/toolArgs.ts)  ═══
    // ════════════════════════════════════════════════════════════════════════════

    // ── utils/toolArgs.ts — the one-line arg summary under a tool-call title ──
    // `$ ` is a shell prompt marker, deliberately outside the message.
    'chat.args.bashBuilding': '$ (building command...)',
    'chat.args.editCount': { one: '{count} edit', other: '{count} edits' },
    'chat.args.taskComplete': '(task complete)',
    'chat.args.createTask': '(create task)',
    'chat.args.editTask': '(edit task)',
    'chat.args.question': '(question)',
    'chat.args.viewTasks': '(list backlog tasks)',
    /** Placeholder for a path the model has not streamed yet. */
    'chat.args.fileWord': 'file',
    'chat.args.lspHover': 'hover {sym} in {path}',
    'chat.args.lspHoverIn': '(hover in {path})',
    'chat.args.lspReferences': 'references for {sym} in {path}',
    'chat.args.lspReferencesIn': '(references in {path})',
    'chat.args.lspDefinition': 'definition of {sym} in {path}',
    'chat.args.lspDefinitionIn': '(definition in {path})',
    'chat.args.lspRenameTo': 'rename {sym} to {newName} in {path}',
    'chat.args.lspRename': 'rename {sym} in {path}',
    'chat.args.lspRenameIn': '(rename in {path})',

    // ── utils/chat-entries.ts + services/stream-handler.ts ──
    // Fallback shown when an error event carries no message. `isRepeatError()` compares two
    // values errorFromEvent() produced, so a translated fallback still dedupes.
    'chat.error.fallback': 'An error occurred',
    'chat.merge.failed': 'Merge failed',
    'chat.merge.conflictsResolved': 'Merge conflicts resolved',
    'chat.merge.merged': 'Merged',
    'chat.advisory.diagnostics': 'Workspace diagnostics',
    'chat.advisory.mergeConflict': 'Merge conflict',
    'chat.advisory.note': 'Environment note',
    'chat.stream.sandboxImage': 'sandbox image',
    'chat.stream.pullingImage': 'Pulling {name}...',
    'chat.stream.buildingImage': 'Building sandbox image from Dockerfile...',
    'chat.stream.startingContainer': 'Starting sandbox container...',
    'chat.stream.summarized': 'Context was summarized to free space',

    // ── shared across the chat surface ──
    'chat.common.cancel': 'Cancel',
    'chat.common.save': 'Save',
    'chat.common.send': 'Send',
    'chat.common.delete': 'Delete',
    'chat.common.close': 'Close',
    'chat.common.remove': 'Remove',
    'chat.common.attachImage': 'Attach image',
    'chat.common.attachment': 'Attachment',
    'chat.common.loading': 'Loading...',
    'chat.common.newThread': 'New Thread',

    // ── Chat/MessageStats.svelte ──
    // The row is `{model} · {tokens} · {seconds}` — separators, order and the completion-token
    // semantics are fixed by the Chat UI rules, so only the labels are keys here. English has
    // always said "1 tokens", so `one` keeps the plural form: introducing "1 token" would
    // change the English output every chat-messages locator reads.
    'chat.stats.tokens': '{count} tokens',
    'chat.stats.seconds': '{seconds}s',
    'chat.stats.modelFallback': 'model',

    // ── Chat/MessageActions.svelte (aria-label and title share one message) ──
    'chat.actions.copy': 'Copy message',
    'chat.actions.forkAria': 'Start new thread from this message',
    'chat.actions.forkTitle': 'Edit / new thread',
    'chat.actions.delete': 'Delete message and all following messages',
    'chat.actions.regenerate': 'Regenerate response',

    // ── Chat/QueueIndicator.svelte ──
    'chat.queue.targetAgent': 'agent',
    'chat.queue.targetLlm': 'LLM provider',
    'chat.queue.position': 'Awaiting {target} — position {position} in queue, ~{wait}s',
    'chat.queue.errorParen': ' ({error})',
    'chat.queue.connectionIssue': 'Connection issue{errPart} — retry {count}',
    'chat.queue.promptProgress': 'Awaiting {target}... Processing Prompt: {percent}%',
    'chat.queue.awaiting': 'Awaiting {target}...',

    // ── Chat/ContextUsage.svelte (donut tooltip; the suffix keeps its leading separator) ──
    'chat.context.usage': '{used} / {limit} tokens · {percent}%',
    'chat.context.usedTokens': '{used} tokens',
    'chat.context.clickToSummarize': ' · click to summarize',
    'chat.context.clickToCancel': ' · click to cancel',
    'chat.context.sessionCost': 'Session cost',

    // ── Chat/ReasoningBlock.svelte ──
    // English split this as "{Hide|Show} reasoning" across two text nodes; one message each.
    'chat.reasoning.hide': 'Hide reasoning',
    'chat.reasoning.show': 'Show reasoning',
    'chat.reasoning.loading': 'Loading reasoning...',

    // ── Chat/CheckpointDivider.svelte ──
    'chat.checkpoint.label': 'Checkpoint',
    'chat.checkpoint.viewChanges': 'View changes up to this checkpoint',

    // ── Chat/ThreadSelector.svelte ──
    'chat.thread.switch': 'Switch thread',
    /** Fallback name of the root thread when it is not (yet) in the thread list. */
    'chat.thread.main': 'Main',

    // ── ModeSelect.svelte + the mode toggle in ChatForm / StartPanel ──
    // The `act|plan|explore` values sent to the server are data; these are the labels.
    'chat.mode.act': 'Act',
    'chat.mode.plan': 'Plan',
    'chat.mode.explore': 'Explore',
    'chat.mode.aria': 'Operating mode',
    'chat.mode.toggleAria': 'Toggle operating mode',
    'chat.mode.actTip': 'Act mode: AI is required to provide structured responses during review, and has read/write access. Click to switch.',
    'chat.mode.planTip': 'Plan mode: AI decomposes work into subtasks on the board, and only has read access. Click to switch.',
    'chat.mode.exploreTip': 'Explore mode: AI has read/write access. Click to switch.',

    // ── ChangeModelModal.svelte ──
    'chat.changeModel.title': { one: 'Change Model for {count} task', other: 'Change Model for {count} tasks' },
    'chat.changeModel.confirm': 'Change Model',
    'chat.changeModel.confirmTip': 'Change the model',
    'chat.changeModel.model': 'Model',
    'chat.changeModel.noProviders': 'No providers configured',

    // ── Chat/StartPanel.svelte ──
    'chat.start.title': 'Start the AI task',
    'chat.start.titleWithShortcut': 'Start the AI task ({shortcut})',
    'chat.start.button': 'Start Task',
    'chat.start.starting': 'Starting...',

    // ── Chat/EditForm.svelte ──
    'chat.edit.contentAria': 'Edit message content',
    'chat.edit.cancelEditing': 'Cancel editing',

    // ── Chat/ChatForm.svelte ──
    /** Shown only when the caller passes no placeholder. */
    'chat.form.placeholder': 'Type a message...',
    /** What the task chat asks for instead. */
    'chat.form.taskPlaceholder': 'Send a message...',
    'chat.form.input': 'Message input',
    'chat.form.removeImage': 'Remove image',
    'chat.form.stop': 'Stop generation',
    'chat.form.send': 'Send message',
    'chat.form.couldNotRead': 'Could not read {name}',

    // ── Chat/ModelSelector.svelte ──
    // The bare 'Agent' / 'Provider' fallbacks reuse settings.provider.agentFallback /
    // settings.provider.providerFallback — the settings batch owns those strings, and the two
    // surfaces share them by key rather than by copy.
    'chat.model.selectProvider': 'Select provider',
    'chat.model.selectModel': 'Select model',
    'chat.model.loadingModels': 'Loading models...',
    'chat.model.none': 'No models available',
    'chat.model.count': { one: '{count} model', other: '{count} models' },

    // ── Chat/CompressContextDialog.svelte ──
    'chat.compress.title': 'Compress Context',
    'chat.compress.body': 'This will create a summary of the conversation to make more efficient use of context space. You can delete the summary to restore the full context.',
    'chat.compress.contextUsed': 'Context used',
    'chat.compress.percentage': 'Percentage',
    'chat.compress.confirm': 'Compress',

    // ── Chat/ReviewBar.svelte + the commit dialog's confirm button ──
    // Commit wording is load-bearing: the click means commit + merge + Done and a merge conflict
    // only interrupts it (docs/commit-and-done.md), so these must keep saying "and move to Done"
    // and must never read like a cancellation.
    'chat.review.commitAndDone': 'Commit and move to Done',
    'chat.review.commitAndDoneTip': 'Commit changes and move task to Done',
    'chat.review.committing': 'Committing...',
    'chat.review.inQueue': 'In queue...',
    'chat.review.resume': 'Resume',
    'chat.review.resuming': 'Resuming...',
    'chat.review.resumePausedTip': 'Resume the paused task',
    'chat.review.resumeFromMessageTip': 'Resume the task from the last user message',
    'chat.review.restart': 'Restart Task',
    'chat.review.restarting': 'Restarting...',
    'chat.review.restartTip': 'Restart the task from the last user message',
    'chat.review.moveToReview': '→ Review',
    'chat.review.moveToReviewTip': 'Move task to Review',
    'chat.review.moveToDone': '→ Done',
    'chat.review.moveToDoneTip': 'Move task to Done',
    'chat.review.moveToBacklog': '→ Backlog',
    'chat.review.moveToBacklogTip': 'Move task to Backlog',
    'chat.review.moveToDoneButton': 'Move to Done',
    'chat.review.moving': 'Moving...',

    // ── Chat/CommitDialog.svelte ──
    // 'Committing as' / 'Merge into' keep their colon: the value is appended by the template,
    // so the message stays a label rather than a half-sentence.
    'chat.commit.title': 'Commit changes',
    'chat.commit.desc': 'Review and edit the commit message, then confirm to commit changes and move the task to Done.',
    'chat.commit.messageAria': 'Commit message',
    'chat.commit.placeholder': 'Enter commit message...',
    'chat.commit.generating': 'Generating commit message...',
    'chat.commit.generatingShort': 'Generating...',
    'chat.commit.promptProgress': 'Processing prompt: {percent}%',
    'chat.commit.committingAs': 'Committing as:',
    'chat.commit.mergeInto': 'Merge into:',

    // ── Chat/ToolCall.svelte ──
    // The pending progress line used to be a bare word with `: {path}` / `: {bytes}` bolted on
    // by the markup. Each shape is its own message so another language can order the clauses.
    'chat.tool.runningFor': 'Running for {elapsed}',
    'chat.tool.writingSummary': 'Writing Summary',
    'chat.tool.writingQuestion': 'Writing Question',
    'chat.tool.running': 'Running',
    'chat.tool.runningPath': 'Running: {path}',
    'chat.tool.preparing': 'Preparing',
    'chat.tool.preparingPath': 'Preparing: {path}',
    'chat.tool.editing': 'Editing...',
    'chat.tool.writingFile': 'Writing...',
    'chat.tool.writing': 'Writing',
    'chat.tool.writingPath': 'Writing: {path}',
    'chat.tool.writingBytes': 'Writing: {bytes}',
    'chat.tool.writingPathBytes': 'Writing: {path}: {bytes}',
    'chat.tool.failed': 'Failed',
    'chat.tool.completed': 'Completed',
    'chat.tool.sandboxed': 'Sandboxed',
    'chat.tool.notSandboxed': 'Not sandboxed',
    'chat.tool.expandArgs': 'Expand arguments',
    'chat.tool.collapseArgs': 'Collapse arguments',
    'chat.tool.wroteTo': 'Wrote {bytes} to:',
    'chat.tool.unknownPath': '(unknown)',
    'chat.tool.createdTask': 'Created task',
    'chat.tool.openTask': 'Open task',
    'chat.tool.editedTask': 'Edited task',
    'chat.tool.backlogTasks': { one: '{count} backlog task', other: '{count} backlog tasks' },
    'chat.tool.autoAnswer': 'Automated answer (parent context)',
    'chat.tool.collapse': 'Collapse',
    'chat.tool.showAll': 'Show all ({lines} lines, {size})',
    'chat.tool.timeout': 'Timeout {elapsed}',

    // ── Chat/ChatEntries.svelte ──
    'chat.entries.region': 'Chat messages',
    'chat.entries.loading': 'Loading conversation…',
    'chat.entries.empty': 'No messages yet',
    'chat.entries.emptyHint': 'Send a message to start the conversation',
    'chat.entries.scrollToMessage': 'Scroll to message',
    'chat.entries.scrollToLatest': 'Scroll to latest message',
    'chat.entries.latest': 'Latest',
    'chat.entries.toggleFullscreen': 'Toggle fullscreen',
    'chat.entries.reconnecting': 'Reconnecting...',
    'chat.entries.dismissError': 'Dismiss error',
    'chat.entries.retryTip': 'Retry connection',
    'chat.entries.retryNow': 'Retry now',
    'chat.entries.summarized': 'Context summarized',
    'chat.entries.buildDone': 'Done',
    'chat.entries.buildOutput': 'Build output',
    'chat.entries.building': 'Building...',

    // ── Chat/ChatMessage.svelte ──
    'chat.message.editTitle': 'Edit message',
    'chat.message.queued': 'Queued — will send when ready',
    // A system line is never dressed up as a user bubble; this is its header.
    'chat.message.system': 'System',
    'chat.message.dismiss': 'Dismiss message',

    // ── Chat/TaskChat.svelte ──
    // Screen-reader live region only.
    'chat.task.connecting': 'Connecting...',
    'chat.task.responding': 'Agent is responding...',
    'chat.task.connected': 'Connected, waiting for messages',
    /** Client-side notices: never persisted, so both halves are safe to translate. */
    'chat.task.sendQueued': 'Connection lost — message queued and will be sent when the connection is restored',
    'chat.task.sendFailed': 'Failed to send message',
    'chat.task.compressFailed': 'Failed to compress context',
    'chat.task.regenerateFailed': 'Failed to regenerate',
    // Reachable counts are always >= 1 (entries.length - index of the clicked message), so the
    // plural table reproduces the old `count > 1 ? 's' : ''` byte for byte.
    'chat.task.deleteConfirm': { one: 'This will delete {count} message. This action cannot be undone.', other: 'This will delete {count} messages. This action cannot be undone.' },
    'chat.task.deleteAlsoBranch': 'This will also delete the git branch "{branch}".',
    'chat.task.deleteTitle': 'Delete Message',

    // ↓ keys from the other owned files of this batch are appended below this line ↓
} satisfies Record<string, MessageValue>;

export type MessageKey = keyof typeof en;
