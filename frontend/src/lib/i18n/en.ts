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
    // ═══ namespace: explorer.* — explorer / file browsers batch                ═══
    // ═══ (ExplorerView.svelte, TaskExplorerView.svelte, FileBrowser.svelte,     ═══
    // ═══  TaskFileBrowser.svelte, BranchFileBrowser.svelte,                     ═══
    // ═══  BranchSymbols.svelte, PathPicker.svelte)                              ═══
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

    // ↓ keys for each owned file of this batch are appended below this line ↓
} satisfies Record<string, MessageValue>;

export type MessageKey = keyof typeof en;
