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
    // ═══ namespace: explorer.* — explorer / file browsers batch                              ═══
    // ═══ (ExplorerView.svelte, TaskExplorerView.svelte, FileBrowser.svelte,                  ═══
    // ═══  TaskFileBrowser.svelte, BranchFileBrowser.svelte,                                  ═══
    // ═══  BranchSymbols.svelte, PathPicker.svelte)                                           ═══
    // ════════════════════════════════════════════════════════════════════════════

    // ── i18n runtime (self-test surface; replaced when T5's catalog lands) ──
    'i18n.test.plain': 'Plain message',
    'i18n.test.files': { one: '{count} file', other: '{count} files' },

    // ── Shared chrome across the explorer components (identical copy in more than
    //    one of them, so it is one key rather than a per-file duplicate) ──
    'explorer.shared.loading': 'Loading...',
    'explorer.shared.cancel': 'Cancel',
    'explorer.shared.discard': 'Discard',
    'explorer.shared.unsavedChanges': 'Unsaved changes',
    'explorer.shared.unsavedDiscardMessage': '"{file}" has unsaved changes. Discard them?',
    'explorer.shared.openTerminal': 'Open terminal',
    'explorer.shared.terminalTitle': 'Open terminal ({shortcut})',
    'explorer.shared.closeEscape': 'Close (Escape)',
    'explorer.shared.reviewChanges': 'Review Changes',
    'explorer.shared.fileBrowser': 'File browser',
    'explorer.shared.editor': 'Editor',
    'explorer.shared.symbols': 'Symbols',
    'explorer.shared.selectFileToEdit': 'Select a file to edit',
    'explorer.shared.resizeSplitAria': 'Resize file browser and git graph panels',
    'explorer.shared.resizeLeftAria': 'Resize left panel',
    'explorer.shared.resizeRightAria': 'Resize right panel',
    'explorer.shared.filesLabel': 'Files',
    'explorer.shared.goHome': 'Go to home directory',
    'explorer.shared.pathPlaceholder': 'Enter path or browse...',
    'explorer.shared.filePathAria': 'File path',
    'explorer.shared.contentsAria': 'Directory contents',
    'explorer.shared.findInFiles': 'Find in files',
    'explorer.shared.findInFilesTitle': 'Find in files (Ctrl+Shift+F)',
    'explorer.shared.newFileTitle': 'New file',
    'explorer.shared.createFileAria': 'Create new file',
    'explorer.shared.newFolder': 'New folder',
    'explorer.shared.createFolderAria': 'Create new folder',
    'explorer.shared.newFileNameAria': 'New file name',
    'explorer.shared.newFolderNameAria': 'New folder name',
    'explorer.shared.filenamePlaceholder': 'filename',
    'explorer.shared.folderNameLowerPlaceholder': 'folder name',
    'explorer.shared.errReadDir': 'Failed to read directory',
    'explorer.shared.errCreateFile': 'Failed to create file',
    'explorer.shared.errCreateFolder': 'Failed to create folder',
    'explorer.shared.errInitGit': 'Failed to initialize git repository',
    // One directory/file entry label, shared by every browser in this batch.
    'explorer.shared.entryFolder': 'Folder: {name}',
    'explorer.shared.entryFile': 'File: {name}',
    'explorer.shared.openFolder': 'Open folder: {name}',

    // ── ExplorerView.svelte — project explorer shell (branch view) ──
    'explorer.view.aria': 'Explorer view',
    'explorer.view.closeAria': 'Close branch view',
    'explorer.view.switchBranch': 'Switch branch',
    'explorer.view.noBranches': 'No branches',

    // ── TaskExplorerView.svelte — same shell, scoped to one task's worktree ──
    'explorer.taskView.aria': 'Task explorer view',
    'explorer.taskView.closeAria': 'Close task explorer',

    // ── FileBrowser.svelte — the add-project / directory-picker modal ──
    'explorer.fileBrowser.title': 'Add Project',
    'explorer.fileBrowser.browse': 'Browse',
    'explorer.fileBrowser.browseTitle': 'Browse for an existing directory',
    'explorer.fileBrowser.cloneTab': 'Clone from Git',
    'explorer.fileBrowser.cloneTitle': 'Clone a git repository',
    'explorer.fileBrowser.folderNamePlaceholder': 'Folder name',
    'explorer.fileBrowser.create': 'Create',
    'explorer.fileBrowser.isGitRepo': '✓ This is a git repository',
    // Entry labels are whole messages rather than a stem plus a suffix, so a
    // translator can move the git marker wherever their language puts it.
    'explorer.fileBrowser.entryFolderGit': 'Folder: {name}, git repository',
    'explorer.fileBrowser.entryFileGit': 'File: {name}, git repository',
    'explorer.fileBrowser.openFolderGit': 'Open folder: {name} (git repository)',
    'explorer.fileBrowser.entryFileGitTitle': 'File: {name} (git repository)',
    'explorer.fileBrowser.cloneUrl': 'Git repository URL',
    // The clone help wraps an example URL in <code>, so it is a before/after pair;
    // the trailing space of the first half is load-bearing (it separates the text
    // from the <code> element).
    'explorer.fileBrowser.cloneHelpBefore': 'Supports HTTPS and SSH URLs (e.g. ',
    'explorer.fileBrowser.cloneHelpAfter': ').',
    'explorer.fileBrowser.cloneInto': 'Clone into',
    'explorer.fileBrowser.useSandbox': 'Use Sandbox',
    'explorer.fileBrowser.select': 'Select',
    'explorer.fileBrowser.selectTitle': 'Select this directory as project',
    'explorer.fileBrowser.initGit': 'Initialize Git',
    'explorer.fileBrowser.initializing': 'Initializing...',
    'explorer.fileBrowser.initGitTitle': 'Initialize git repository in this directory',
    'explorer.fileBrowser.clone': 'Clone',
    'explorer.fileBrowser.cloning': 'Cloning...',
    'explorer.fileBrowser.cloneIntoDirTitle': 'Clone repository into the current directory',
    'explorer.fileBrowser.cloneNeedUrl': 'Please enter a git repository URL',

    // ── TaskFileBrowser.svelte — the file tree of one task's worktree ──
    'explorer.taskFiles.empty': 'Empty folder',

    // ── BranchFileBrowser.svelte — the project explorer's file tree ──
    'explorer.branchFiles.initSandbox': 'Initializing sandbox...',
    'explorer.branchFiles.empty': 'Empty directory',
    'explorer.branchFiles.treeAria': 'File tree',
    'explorer.branchFiles.gitStatusTitle': 'Git: {status}',
    'explorer.branchFiles.gitStatusAria': 'Git status: {status}',

    // ── BranchSymbols.svelte — symbol / document-outline panel ──
    'explorer.symbols.sortByPosition': 'Sort by position',
    'explorer.symbols.sortAlphabetically': 'Sort alphabetically',
    'explorer.symbols.switchToPosition': 'Switch to position sort',
    'explorer.symbols.switchToAlphabetical': 'Switch to alphabetical sort',
    'explorer.symbols.flatList': 'Flat list',
    'explorer.symbols.treeView': 'Tree view',
    'explorer.symbols.switchToFlat': 'Switch to flat list',
    'explorer.symbols.switchToTree': 'Switch to tree view',
    'explorer.symbols.outlineLabel': 'Outline',
    'explorer.symbols.noneHeadings': 'No headings',
    'explorer.symbols.noneSymbols': 'No symbols',
    'explorer.symbols.outlineAria': 'Document outline',
    'explorer.symbols.treeAria': 'Symbol tree',
    'explorer.symbols.outlineListAria': 'Symbol outline',
    'explorer.symbols.expand': 'Expand',
    'explorer.symbols.collapse': 'Collapse',
    'explorer.symbols.lineTitle': 'Line {line}: {name}',

    // ── PathPicker.svelte — generic directory/file picker modal. Its `title`,
    //    `emptyLabel` and `selectTitle` arrive as props from the calling screen and
    //    are that screen's copy to own, so they stay props and are never keyed here. ──
    'explorer.pathPicker.select': 'Select',
    'explorer.pathPicker.selectFile': 'Select file: {name}',
    'explorer.pathPicker.dirPathAria': 'Directory path',
    'explorer.pathPicker.goParentAria': 'Go to parent directory',

    // ════════════════════════════════════════════════════════════════════════════
    // ═══ namespace: settings.* — Settings / ConfigModal / Extensions batch                   ═══
    // ═══ (ConfigModal.svelte, ConfigModal/*.svelte, StatsModal.svelte,                       ═══
    // ═══  utils/providers.ts)                                                                ═══
    // ════════════════════════════════════════════════════════════════════════════
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
    // ═══ namespace: app.* — app shell + shared primitives batch                              ═══
    // ═══ (App.svelte, App/Sidebar.svelte, App/TopBar.svelte, LoginModal,                     ═══
    // ═══  CreateTaskModal, ConfirmationDialog, stores/dialogs.svelte.ts,                     ═══
    // ═══  constants/modes.ts, utils/keyboard.ts)                                             ═══
    // ═══                                                                                     ═══
    // ═══ No key here for `aria-label` / `title` / `placeholder` copy — that                  ═══
    // ═══ text stays a literal English string in the component (see README.md).               ═══
    // ════════════════════════════════════════════════════════════════════════════

    // ── shared button / dialog copy ──
    'app.cancel': 'Cancel',
    'app.discard': 'Discard',
    'app.keepEditing': 'Keep editing',
    'app.confirm.confirm': 'Confirm',
    'app.dialog.compress': 'Compress',
    'app.dialog.commitAndDone': 'Commit and move to Done',

    // ── utils/keyboard.ts — keycap legends and shortcut tooltips ──
    // The legends are what a keycap hint shows (Column, StartPanel, the Create
    // button); the binding itself stays the normalized `control+k` form.
    'app.key.control': 'Ctrl',
    'app.key.meta': 'Cmd',
    'app.key.shift': 'Shift',
    'app.key.alt': 'Alt',
    'app.key.space': 'Space',
    'app.key.click': 'Click',
    'app.key.enter': 'Enter',
    'app.key.tab': 'Tab',
    'app.key.backspace': 'Backspace',
    'app.key.delete': 'Delete',
    'app.key.up': 'Up',
    'app.key.down': 'Down',
    'app.key.left': 'Left',
    'app.key.right': 'Right',
    'app.shortcut.toggleFullscreen': 'Toggle fullscreen',
    'app.shortcut.toggleFullscreenWithKey': 'Toggle fullscreen ({key})',
    'app.shortcut.openTerminal': 'Open terminal',
    'app.shortcut.openTerminalWithKey': 'Open terminal ({key})',

    // ── constants/modes.ts — operating-mode descriptions ──
    // Read through `MODE_DESCRIPTIONS`, whose getters keep the export shape (and
    // therefore the two call sites this batch does not own) unchanged.
    'app.mode.act': 'AI is required to provide structured responses during review, and has read/write access.',
    'app.mode.plan': 'AI decomposes work into subtasks on the board, and only has read access.',
    'app.mode.explore': 'AI has read/write access.',

    // ── App/Sidebar.svelte ──
    'app.sidebar.projects': 'Projects',
    'app.sidebar.addProject': '+ Add Project',
    'app.sidebar.fullScreen': 'Full Screen',
    'app.sidebar.tokenUsage': 'Token Usage',
    'app.sidebar.settings': 'Settings',

    // ── LoginModal.svelte ──
    // Pre-auth: the runtime answers this from the statically bundled `en` catalog,
    // so the login screen needs no fetch and no extension/config load (see README).
    'app.login.signIn': 'Sign in',
    'app.login.signingIn': 'Signing in...',
    'app.login.failed': 'Login failed',
    'app.login.networkError': 'Network error',

    // ── CreateTaskModal.svelte ──
    'app.task.create': 'Create Task',
    'app.task.createSubtask': 'Create Subtask',
    'app.task.creating': 'Creating...',
    'app.task.autoStart': 'Auto start',
    'app.task.subtaskOf': 'Subtask of:',
    'app.task.parentFallback': 'Parent task',
    'app.task.description': 'Description',
    'app.task.model': 'Model',
    'app.task.operatingMode': 'Operating mode',
    'app.task.repository': 'Repository',
    'app.task.branch': 'Branch',
    'app.task.branchDesc': "Base branch for this task's changes",
    'app.task.autoMerge': 'Auto-merge',
    'app.task.autoMergeParentDesc': 'Merge changes into parent task when complete',
    // The branch badge is an inline element inside this sentence, so the copy is
    // two messages — the other order is the translator's to choose.
    'app.task.autoMergeInto.before': 'Merge changes into',
    'app.task.autoMergeInto.after': 'when complete',
    'app.task.subtasksAutoMerge': 'Allow subtasks to auto-merge',
    'app.task.subtasksAutoMergeDesc': 'Subtasks created under this task will auto-merge into it',
    'app.task.autoStartFailed': 'Task created, but auto-start failed.',
    'app.task.queuedOffline': 'Offline — task creation and start are queued; it will run when you are back online.',
    'app.task.discardTitle': 'Discard task?',
    'app.task.discardMessage': 'Your task description has not been saved. If you close now, your changes will be lost.',

    // ── App.svelte — page title, banners, offline screen, empty states ──
    'app.title.default': 'Kanban Board',
    // The manual `task${n === 1 ? '' : 's'}` this replaces is exactly the shape
    // `Intl.PluralRules` exists for.
    'app.title.review': {
        one: '🟡 {name} - {count} task for review',
        other: '🟡 {name} - {count} tasks for review',
    },
    'app.time.justNow': 'just now',
    'app.project.removeTitle': 'Remove Project',
    'app.project.removeMessage': "Remove \"{name}\" from the board?\n\nThis will delete the project's Kanban board, all tasks, messages, and their worktree directories.\nThe project folder itself will NOT be deleted.",
    'app.project.remove': 'Remove',
    'app.offline.title': 'Server unreachable',
    'app.offline.button': 'Offline Mode',
    'app.reconnect.status': 'Reconnecting ...',
    'app.reconnect.statusCountdown': 'Reconnecting in {seconds}s ...',
    'app.reconnect.dataFrom': 'Data from {time}',
    'app.reconnect.connect': 'Connect',
    'app.cache.banner': 'Viewing cached data',
    'app.skipToMain': 'Skip to main content',
    'app.empty.welcomeTitle': 'Welcome to Kanban',
    'app.empty.welcomeBody': 'Configure an LLM provider to get started. Supports any OpenAI-compatible API.',
    'app.empty.configureProvider': 'Configure Provider',
    'app.empty.noProjectsTitle': 'No Projects',
    'app.empty.noProjectsBody': 'Add a project to get started. Projects must be git repositories.',
    'app.empty.addProject': 'Add Project',

    // ↓ keys for each owned file of this batch are appended below this line ↓

    // ════════════════════════════════════════════════════════════════════════════
    // ═══ namespace: api.* — request-failure fallback text                                    ═══
    // ═══ (api/*.ts, services/extension-manager.svelte.ts)                                    ═══
    // ════════════════════════════════════════════════════════════════════════════

    // The server's own `body.error` always wins when present; these are what the browser
    // shows when the response carried no message. Never wrap server text in `t()`.
    'api.fetchConfig': 'Failed to fetch config',
    'api.testConnection': 'Failed to test connection',
    'api.fetchModels': 'Failed to fetch models',
    'api.fetchExtensions': 'Failed to fetch extensions',
    'api.updateExtension': 'Failed to update extension',
    'api.deleteExtension': 'Failed to delete extension',
    'api.updateExtensionEnabledState': 'Failed to update extension enabled state',
    'api.updateDependencyDirectory': 'Failed to update the dependency directory',
    'api.readHomeDirectory': 'Failed to read the home directory',
    'api.fetchLocalExtensions': 'Failed to fetch local extensions',
    'api.fetchRegistry': 'Failed to fetch registry',
    'api.checkForUpdates': 'Failed to check for updates',
    'api.operationFailed': 'Operation failed',
    'api.unexpectedResponse': 'Unexpected response from {path} ({contentType}): {body}',
    'api.emptyBody': 'empty body',
    'api.noReader': 'No reader available',
    'api.operationEndedEarly': 'Operation ended before reporting completion',
    'api.fetchGitLog': 'Failed to fetch git log',
    'api.fetchCommitDiff': 'Failed to fetch commit diff',
    'api.fetchBranches': 'Failed to fetch branches',
    'api.checkoutBranch': 'Failed to checkout branch',
    'api.fetchGitStatus': 'Failed to fetch git status',
    'api.fetchProjectDiff': 'Failed to fetch project diff',
    'api.fetchFile': 'Failed to fetch file',
    'api.stageFile': 'Failed to stage file',
    'api.unstageFile': 'Failed to unstage file',
    'api.commit': 'Failed to commit',
    'api.pull': 'Failed to pull',
    'api.push': 'Failed to push',
    'api.fetchDiff': 'Failed to fetch diff',
    'api.fetchCommits': 'Failed to fetch commits',
    'api.fetchFileContent': 'Failed to fetch file content',
    'api.fetchTaskGitLog': 'Failed to fetch task git log',
    'api.fetchTaskCommitDiff': 'Failed to fetch task commit diff',
    'api.createProject': 'Failed to create project',
    'api.updateProject': 'Failed to update project',
    'api.buildSandboxImage': 'Failed to build sandbox image',
    'api.streamingNotSupported': 'Streaming not supported',
    'api.buildCompletedWithoutResult': 'Build completed without result',
    'api.deleteProject': 'Failed to delete project',
    'api.resetStats': 'Failed to reset stats',
    'api.createTask': 'Failed to create task',
    'api.updateTask': 'Failed to update task',
    'api.deleteTask': 'Failed to delete task',
    'api.moveTask': 'Failed to move task',
    'api.commitAndDone': 'Failed to commit and mark as done',
    'api.clearDoneTasks': 'Failed to clear done tasks',
    'api.initializeTask': 'Failed to initialize task',
    'api.startTask': 'Failed to start task',
    'api.startSubtask': 'Failed to start subtask',
    'api.stopTask': 'Failed to stop task',
    'api.retryTask': 'Failed to retry task',
    'api.resumeTask': 'Failed to resume task',
    'api.sendMessage': 'Failed to send message',
    'api.createMessage': 'Failed to create message',
    'api.deleteMessages': 'Failed to delete messages',
    'api.deleteMessage': 'Failed to delete message',
    'api.updateMessage': 'Failed to update message',
    'api.updateConfig': 'Failed to update config',
    'api.switchThread': 'Failed to switch thread',
    'api.forkThread': 'Failed to fork thread',
    'api.fetchProjects': 'Failed to fetch projects',
    'api.fetchProject': 'Failed to fetch project',
    'api.fetchProjectCounts': 'Failed to fetch project counts',
    'api.fetchProjectStats': 'Failed to fetch project stats',
    'api.cloneProject': 'Failed to clone project',
    // The clone stream's `error` event may carry no message; this is what shows then.
    'api.cloneFailed': 'clone failed',
    'api.cloneCompletedWithoutResult': 'Clone completed without result',
    'api.fetchBoard': 'Failed to fetch board',
    'api.taskNotFound': 'Task not found',
    'api.fetchMessages': 'Failed to fetch messages',
    'api.fetchMessage': 'Failed to fetch message',
    'api.checkMessageDelete': 'Failed to check message delete',
    'api.summarizeTask': 'Failed to summarize task',
    'api.compactTask': 'Failed to compact task',
    'api.fetchSessions': 'Failed to fetch sessions',
    'api.revokeSession': 'Failed to revoke session',
    'api.revokeOtherSessions': 'Failed to revoke other sessions',
    'api.fetchThreads': 'Failed to fetch threads',
    'api.loadExtensions': 'Failed to load extensions',
    'api.loadRegistry': 'Failed to load registry',
    'api.installExtension': 'Failed to install extension',
    // ════════════════════════════════════════════════════════════════════════════
    // ═══ namespace: sandbox.* — sandbox surface batch                                        ═══
    // ═══ (SandboxConfigModal, SandboxPanel, SandboxImageStatusBanner, utils/sandbox-image-status) ═══
    // ════════════════════════════════════════════════════════════════════════════

    // Load-bearing security copy: "Read-only"/"Read/Write" and the disable-sandbox confirmation. Tests
    // match the rendered English (/read-only/). Capability NAMES (CAP_*), container/host paths and port
    // numbers are never catalog entries — only the human description beside a name is. The
    // "set DANGER_ZONE=1 to allow" refusal is the Go server's own message and stays untranslated.
    'sandbox.title': 'Sandbox Configuration',
    'sandbox.lockedNotice': 'Cannot change sandbox settings while tasks are In Progress. The settings below are shown for reference only.',
    'sandbox.enabled': 'Enabled',
    'sandbox.disabled': 'Disabled',
    'sandbox.default': 'Default',
    'sandbox.empty': '(empty)',
    'sandbox.isolation': 'Sandbox Isolation',
    'sandbox.isolationAria': 'Sandbox isolation',
    'sandbox.isolationDesc': 'Use gVisor to isolate all AI actions within a sandbox',
    'sandbox.networkIsolation': 'Network Isolation',
    'sandbox.networkIsolationAria': 'Network isolation',
    'sandbox.networkIsolationDesc': 'Block all network access',
    'sandbox.memoryLimit': 'Memory Limit',
    'sandbox.memoryLimitDesc': 'Max container memory (MB) — blank = no limit',
    'sandbox.memoryLimitAria': 'Maximum container memory in MB',
    'sandbox.noLimit': 'No limit',
    'sandbox.image': 'Image',
    'sandbox.forwardedPorts': 'Forwarded Ports',
    'sandbox.activePortMappings': 'Active Port Mappings',
    'sandbox.customMounts': 'Custom Mounts',
    'sandbox.environmentVariables': 'Environment Variables',
    'sandbox.capabilities': 'Capabilities',
    'sandbox.hostPort': 'Host port',
    'sandbox.containerPort': 'Container port',
    'sandbox.hostPath': 'Host path',
    'sandbox.containerPath': 'Container path',
    'sandbox.readWrite': 'Read/Write',
    'sandbox.readOnly': 'Read-only',
    'sandbox.mountReadWrite': 'Mount Read/Write',
    'sandbox.mountReadOnly': 'Mount Read-only',
    'sandbox.remove': 'Remove',
    'sandbox.cancelEdit': 'Cancel edit',
    'sandbox.cancel': 'Cancel',
    'sandbox.save': 'Save',
    'sandbox.saving': 'Saving...',
    // Forwarded ports
    'sandbox.ports': 'Forward Ports',
    'sandbox.portsAria': 'Toggle port forwarding settings',
    'sandbox.portsDesc': "Host ports are auto-assigned when tasks run, and visible in the task's Sandbox Info panel.",
    'sandbox.portsNoActive': 'Host ports assigned when tasks run',
    'sandbox.addPort': 'Add port forward',
    'sandbox.savePort': 'Save port',
    'sandbox.portRangeError': 'Port must be between 1 and 65535',
    // Custom mounts
    'sandbox.mountsAria': 'Toggle mount settings',
    'sandbox.mountsDesc': 'Bind mount host paths into sandbox containers.',
    'sandbox.addMount': 'Add mount',
    'sandbox.saveMount': 'Save mount',
    'sandbox.browseHostDirectory': 'Browse host directory',
    'sandbox.mountHostPathError': 'Host path is required',
    'sandbox.mountContainerPathError': 'Container path is required',
    // Environment variables
    'sandbox.envsAria': 'Toggle environment variables settings',
    'sandbox.envsDesc': 'Set environment variables in sandbox containers.',
    'sandbox.addVariable': 'Add variable',
    'sandbox.saveVariable': 'Save variable',
    'sandbox.envNameAria': 'Variable name',
    'sandbox.envValueAria': 'Variable value',
    'sandbox.envKeyPlaceholder': 'KEY',
    'sandbox.envValueInputPlaceholder': 'value',
    'sandbox.envNameError': 'Environment variable name is required',
    'sandbox.envDuplicateError': 'Duplicate variable name: {name}',
    // Capabilities
    'sandbox.capsAria': 'Toggle capabilities settings',
    'sandbox.capsDesc': 'Linux capabilities to add to the container. By default, all capabilities are dropped (--cap-drop=ALL).',
    'sandbox.addCapabilityAria': 'Add capability',
    'sandbox.capSuggestionPlaceholder': 'Type capability name, e.g. CAP_NET_RAW',
    'sandbox.capAddMorePlaceholder': 'Add more...',
    'sandbox.removeCapability': 'Remove {cap}',
    // Memory / image source
    'sandbox.imageSource': 'Image Source',
    'sandbox.imageSourceDesc': 'Container image for sandbox',
    'sandbox.imagePrebuilt': 'Pre-built',
    'sandbox.imageName': 'Image Name',
    'sandbox.imageNameDesc': 'Image for sandbox containers — must name its registry, e.g. docker.io/library/ubuntu:24.04',
    'sandbox.imageNameAria': 'Sandbox image name',
    'sandbox.dockerfile': 'Dockerfile',
    'sandbox.dockerfileDesc': 'Select a Dockerfile on the server',
    'sandbox.disableCache': 'Disable cache',
    'sandbox.skipCacheTitle': 'Skip the container build layer cache for the next build',
    'sandbox.disableCacheAria': 'Disable build cache',
    'sandbox.buildImage': 'Build image',
    'sandbox.build': 'Build',
    'sandbox.building': 'Building...',
    'sandbox.rebuild': 'Rebuild',
    'sandbox.rebuildImage': 'Rebuild image',
    'sandbox.rebuildImageNoCache': 'Rebuild image without cache',
    'sandbox.noDockerfileSelected': 'No Dockerfile selected',
    'sandbox.browse': 'Browse',
    'sandbox.browseDockerfile': 'Browse for Dockerfile',
    'sandbox.buildNotice': 'Build running in background — you can close this dialog.',
    'sandbox.buildOutput': 'Build output',
    'sandbox.imageTagLabel': 'Image tag:',
    'sandbox.saveWarning': 'You have un-added changes — click + to add them before saving.',
    'sandbox.dockerfileRequired': 'Please select a Dockerfile before saving.',
    'sandbox.disableTitle': 'Disable Sandbox',
    'sandbox.disableConfirm': 'Disable sandbox isolation for "{name}"?\n\nTasks will run without gVisor sandboxing, executing commands directly on the host.',
    'sandbox.disableConfirmButton': 'Disable',
    'sandbox.selectDockerfile': 'Select Dockerfile',
    'sandbox.selectDockerfileTitle': 'Select this Dockerfile',
    'sandbox.noDockerfilesFound': 'No Dockerfiles found in this directory',
    'sandbox.selectDirectory': 'Select Directory',
    'sandbox.selectDirectoryTitle': 'Select this directory',
    'sandbox.noSubdirectories': 'No subdirectories in this path',
    // One description per Linux capability name — the NAME is the machine value, never translated.
    'sandbox.cap.CAP_CHOWN': 'Change file ownership',
    'sandbox.cap.CAP_DAC_OVERRIDE': 'Bypass file permission checks',
    'sandbox.cap.CAP_DAC_READ_SEARCH': 'Bypass read permission on files/dirs',
    'sandbox.cap.CAP_FOWNER': 'Bypass permission checks requiring inode owner',
    'sandbox.cap.CAP_FSETID': "Don't clear setuid/setgid on file mod",
    'sandbox.cap.CAP_IPC_LOCK': 'Lock memory',
    'sandbox.cap.CAP_IPC_OWNER': 'Bypass IPC ownership checks',
    'sandbox.cap.CAP_KILL': 'Bypass permission checks for signals',
    'sandbox.cap.CAP_LEASE': 'Lease files',
    'sandbox.cap.CAP_LINUX_IMMUTABLE': 'Set immutable file attribute',
    'sandbox.cap.CAP_MKNOD': 'Create special files',
    'sandbox.cap.CAP_NET_ADMIN': 'Perform network-related operations',
    'sandbox.cap.CAP_NET_BIND_SERVICE': 'Bind to ports < 1024',
    'sandbox.cap.CAP_NET_BROADCAST': 'Make socket broadcasts',
    'sandbox.cap.CAP_NET_RAW': 'Use RAW and PACKET sockets',
    'sandbox.cap.CAP_SETFCAP': 'Set file capabilities',
    'sandbox.cap.CAP_SETGID': 'Manipulate process group IDs',
    'sandbox.cap.CAP_SETPCAP': 'Transfer capabilities',
    'sandbox.cap.CAP_SETUID': 'Manipulate process user IDs',
    'sandbox.cap.CAP_SYS_CHROOT': 'Use chroot()',
    'sandbox.cap.CAP_SYS_PTRACE': 'Trace arbitrary processes',
    'sandbox.cap.CAP_SYS_ADMIN': 'Perform a wide range of system admin operations',
    'sandbox.cap.CAP_SYS_BOOT': 'Reboot the system',
    'sandbox.cap.CAP_SYS_MODULE': 'Load/unload kernel modules',
    'sandbox.cap.CAP_SYS_NICE': 'Raise process nice priority',
    'sandbox.cap.CAP_SYS_RAWIO': 'Perform raw I/O ports',
    'sandbox.cap.CAP_SYS_RESOURCE': 'Override resource limits',
    'sandbox.cap.CAP_SYS_TIME': 'Set system clock',
    'sandbox.cap.CAP_SYS_TTY_CONFIG': 'Configure TTY devices',
    'sandbox.cap.CAP_BPF': 'Use BPF operations',
    'sandbox.cap.CAP_PERFMON': 'Use performance monitoring',
    'sandbox.cap.CAP_AUDIT_CONTROL': 'Modify kernel audit configuration',
    'sandbox.cap.CAP_AUDIT_READ': 'Read kernel audit log',
    'sandbox.cap.CAP_AUDIT_WRITE': 'Write records to kernel audit log',
    'sandbox.cap.CAP_BLOCK_SUSPEND': 'Use suspend-to-memory/disk',
    'sandbox.cap.CAP_CHECKPOINT_RESTORE': 'Checkpoint and restore',
    'sandbox.cap.CAP_MAC_ADMIN': 'Override MAC configuration',
    'sandbox.cap.CAP_MAC_OVERRIDE': 'Override MAC enforcement',
    'sandbox.cap.CAP_WAKE_ALARM': 'Trigger wake alarms',
    'sandbox.cap.CAP_SYSLOG': 'Perform syslog operations',
    // Sandbox Info panel + the image-preparation banner
    'sandbox.infoTitle': 'Sandbox Info',
    'sandbox.sandboxedTask': 'Sandboxed Task',
    'sandbox.isolationEnabled': 'Sandbox Isolation is enabled for this project',
    'sandbox.memoryUsage': 'Memory Usage',
    'sandbox.peakMemory': 'Peak {mb} MB',
    // The kill count stays marked up in the panel, so the number is not part of the message —
    // `count` only selects the form ("1 process" / "3 processes").
    'sandbox.oomKilled': { one: 'process OOM-killed — memory limit reached', other: 'processes OOM-killed — memory limit reached' },
    'sandbox.loadingSettings': 'Loading settings...',
    'sandbox.portsIdle': 'Container has not started due to idle task or terminal',
    'sandbox.dismiss': 'Dismiss',
    'sandbox.dismissImageError': 'Dismiss sandbox image error',
    'sandbox.imageBuildOutput': 'Image build output',
    'sandbox.imageBuilding': 'Building sandbox image {image}…',
    'sandbox.imagePulling': 'Pulling {image}…',
    'sandbox.imageFailed': 'Sandbox image {image} could not be prepared',
    'sandbox.imagePrepareFailed': 'Sandbox image preparation failed',
    // ════════════════════════════════════════════════════════════════════════════
    // ═══ namespace: terminal.* + boardDnd.* + attachments.*                                  ═══
    // ═══ (TerminalPanel, api/terminal.ts, use-board-dnd, utils/attachments)                  ═══
    // ════════════════════════════════════════════════════════════════════════════

    // Only what a human reads. ANSI escapes, the `resize`/`input`/`output` frame types and the
    // key names a hint chip prints stay exactly as written.
    'terminal.connecting': 'Connecting...',
    'terminal.title': 'Terminal',
    'terminal.close': 'Close terminal',
    // The shortcut hint translates; the element's aria-label stays the plain English name, and the
    // virtual keyboard's key names (Escape, Arrow up…) are key names, not prose.
    'terminal.closeWithShortcut': 'Close terminal ({shortcut})',
    'terminal.activePortMappings': 'Active port mappings (Host : Container)',
    'terminal.copy': 'Copy',
    'terminal.paste': 'Paste',
    'terminal.noProject': 'No project selected',
    'boardDnd.cannotMoveToBacklog': 'Cannot move to Backlog: parent task is In Progress',
    // ─── boardDnd/* + attachments/* — drag-to-move toast + the image-picker sentence ───
    // {error} is whatever the request threw (often the server's own sentence) — never translated.
    'boardDnd.moveFailed': 'Failed to move task: {error}',
    'attachments.couldNotRead': 'Could not read {name}',
    'attachments.tooLarge': { one: '{count} image too large (max {limit} MB)', other: '{count} images too large (max {limit} MB)' },
    'attachments.onlyN': 'Only {max} images per message',


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