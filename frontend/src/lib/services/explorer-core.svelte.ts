import { appStore, dialogStore, uiStore } from '$lib/stores';
import { isAnyModalOpen } from '$lib/utils/modal-state';
import { effectiveToggleFullscreenKey, matchShortcut } from '$lib/utils/keyboard';
import { buildLineSelectionPrompt } from '$lib/utils/line-selection';
import {
    clearEditorDraft, clearEditorHistory, loadEditorTabs, loadUnsavedFiles,
    saveEditorTabs, saveUnsavedFiles,
    type EditorTabState,
} from '$lib/storage';
import { revealFileTab, toPersistedTabs, type OpenFileTab } from '$lib/utils/editor-tabs';
import { baseName, isUnderPath } from '$lib/utils/path';
import { EDITOR_SAVE_SETTLED, SAVE_SETTLE_BACKSTOP_MS, SaveRequestGuard } from '$lib/utils/save-state';
import type { LSPSymbol } from '$lib/utils/lsp-symbols';
import type { GitCommit, GitLogResponse } from '$lib/types';
import type { DiffFile } from '$lib/types/diff';
import { t } from '$lib/i18n';

/** Commits per page of history. */
const COMMIT_BATCH_SIZE = 30;
/** Diff base for a commit with no parent. */
const EMPTY_TREE = '4b825dc642cb6eb9a060e54bf899d69f826635b6';
/** Tab state is written after a quiet period: a click through several tabs is one write. */
const TAB_SAVE_DELAY_MS = 300;

export type FsEvent = { op: 'create' | 'delete'; path: string };

/**
 * What App hands either explorer screen, whatever it is browsing. A screen adds its own subject
 * beside these — a project path and branch, or a task — and passes the same decisions to the core.
 */
export interface ExplorerProps {
    projectId: string;
    sandboxEnabled: boolean;
    fsEventQueue: FsEvent[];
    onOpenTerminal: () => void;
    onUpdateFile: (path: string | null) => void;
    changesOpen: boolean;
    onChangesOpen: (open: boolean) => void;
    onClose: () => void;
}

/**
 * What the branch view and the task view answer differently. The core owns everything else about
 * an explorer screen — see the two components for what each edge means in its own terms.
 */
export interface ExplorerEdges {
    /** IndexedDB key of this screen's tab list and unsaved set. */
    storageKey: () => string;
    /** Path prefixes a file must sit under to be openable here. */
    pathRoots: () => string[];
    isMobile: { readonly isMobile: boolean };
    /** File-system events the listing and the open editors are told about. */
    fsEvents: () => FsEvent[];
    /** Project a gutter line-selection becomes a child of; `null` is the board itself. */
    lineSelectionParent: () => string | null;
    updateFile: (path: string | null) => void;
    changesOpen: () => boolean;
    setChangesOpen: (open: boolean) => void;
    closeView: () => void;
    /** One page of history — the caller decides which branch, and whether a file narrows it. */
    fetchLog: (offset: number, limit: number) => Promise<GitLogResponse>;
    fetchCommitDiff: (baseHash: string, headHash: string) => Promise<DiffFile[]>;
    /** Runs before a file opens; false abandons the open. */
    prepareFileOpen?: () => Promise<boolean>;
    /** Take an Escape before the shared order does. True means it was consumed. */
    consumeEscape?: () => boolean;
}

/**
 * The state one explorer screen is made of: tabs, their persistence, the save handshake, the
 * symbol outline, history and the commit-diff overlay. Both explorer components build on one of
 * these, so a fix to any of it lands on both screens.
 */
export class ExplorerCore {
    openFiles = $state<OpenFileTab[]>([]);
    activeFilePath = $state('');
    /** Pinned tabs outlive a click on another file; an unpinned one is a preview that gets replaced. */
    pinnedFiles = $state(new Set<string>());
    unsavedFiles = $state(new Set<string>());

    /**
     * Scroll offsets are plain fields on purpose: `EditorTabs` takes `scrollTops` as a prop and a
     * new object on every scroll event would churn it.
     */
    scrollTops: Record<string, number> = {};
    fileListScroll = 0;

    editorFocused = $state(false);
    editorSaving = $state(false);
    symbols = $state<LSPSymbol[]>([]);
    showFind = $state(false);
    activeTab = $state<'files' | 'editor' | 'symbols' | 'changes'>('files');
    editorFullscreen = $state(false);
    pendingFsEvent = $state<FsEvent | null>(null);
    /** The file listing's scroller, bound by the workspace that renders it. */
    fileBrowserEl = $state<HTMLDivElement | null>(null);

    commits = $state<GitCommit[]>([]);
    originTip = $state('');
    loadingCommits = $state(true);
    hasMoreCommits = $state(false);
    loadingMoreCommits = $state(false);

    diffCommit = $state<GitCommit | null>(null);
    diffCommitIndex = $state(-1);
    diffCommitFiles = $state<DiffFile[]>([]);
    diffLoading = $state(false);

    private readonly edges: ExplorerEdges;
    private readonly saveGuard = new SaveRequestGuard();
    private saveBackstop: ReturnType<typeof setTimeout> | null = null;
    private commitOffset = $state(0);
    private tabSaveTimer: ReturnType<typeof setTimeout> | null = null;
    private lastEventCount = 0;

    constructor(edges: ExplorerEdges) {
        this.edges = edges;

        $effect(() => {
            loadUnsavedFiles(edges.storageKey()).then(paths => { this.unsavedFiles = new Set(paths); }).catch(() => {});
        });

        $effect(() => {
            this.unsavedFiles;
            saveUnsavedFiles(edges.storageKey(), [...this.unsavedFiles]).catch(() => {});
        });

        $effect(() => {
            this.openFiles;
            this.activeFilePath;
            this.pinnedFiles;
            this.queueTabSave();
        });

        $effect(() => {
            if (!this.activeFilePath) this.symbols = [];
        });

        $effect(() => {
            const events = edges.fsEvents();
            const len = events.length;
            if (len === 0) {
                // The queue was cleared (the screen moved to another subject); the last event
                // stays, because the listing has not been told about anything newer.
                this.lastEventCount = 0;
                return;
            }
            if (len > this.lastEventCount) {
                this.pendingFsEvent = events[len - 1];
                this.lastEventCount = len;
            }
        });
    }

    /** The listeners an explorer owns for as long as it is mounted. */
    mount(): void {
        window.addEventListener(EDITOR_SAVE_SETTLED, this.handleSaveSettled);
    }

    unmount(): void {
        window.removeEventListener(EDITOR_SAVE_SETTLED, this.handleSaveSettled);
        if (this.saveBackstop) { clearTimeout(this.saveBackstop); this.saveBackstop = null; }
        this.flushTabSave();
    }

    isPathSafe(path: string): boolean {
        if (!path) return false;
        return this.edges.pathRoots().some(root => path.startsWith(root));
    }

    updateFile(path: string | null): void {
        this.edges.updateFile(path);
    }

    // ---- Tabs ----

    /** Everything the tab list and the unsaved set hold; used when the screen moves to another subject. */
    resetTabs(): void {
        this.openFiles = [];
        this.activeFilePath = '';
        this.pinnedFiles = new Set();
        this.unsavedFiles = new Set();
    }

    loadSavedTabs(): Promise<EditorTabState | null> {
        return loadEditorTabs(this.edges.storageKey());
    }

    /** Adopt a restored list. Which of its tabs survive, and how scroll is merged, is the caller's. */
    applyTabs(openFiles: OpenFileTab[], activeFilePath: string, pinnedFiles: Set<string>): void {
        this.openFiles = openFiles;
        this.activeFilePath = activeFilePath;
        this.pinnedFiles = pinnedFiles;
    }

    queueTabSave(): void {
        if (this.tabSaveTimer) return;
        this.tabSaveTimer = setTimeout(() => {
            this.tabSaveTimer = null;
            this.writeTabState();
        }, TAB_SAVE_DELAY_MS);
    }

    private writeTabState(): void {
        saveEditorTabs(this.edges.storageKey(), {
            openFiles: toPersistedTabs(this.openFiles),
            activeFilePath: this.activeFilePath,
            pinnedFiles: [...this.pinnedFiles],
            scrollTops: this.scrollTops,
            fileListScroll: this.fileListScroll,
        }).catch(() => {});
    }

    /** A pending throttled write is worth more than the screen that queued it. */
    flushTabSave(): void {
        if (!this.tabSaveTimer) return;
        clearTimeout(this.tabSaveTimer);
        this.tabSaveTimer = null;
        this.writeTabState();
    }

    handleEditorScrollTop = (path: string, top: number): void => {
        this.scrollTops[path] = top;
        this.queueTabSave();
    };

    handleFileSelected = (path: string, line?: number, pin = false): void => {
        if (!this.isPathSafe(path)) return;
        const prepare = this.edges.prepareFileOpen;
        if (prepare) {
            void prepare().then(ok => { if (ok) this.openFile(path, line, pin) });
            return;
        }
        this.openFile(path, line, pin);
    }

    private openFile(path: string, line: number | undefined, pin: boolean): void {
        this.closeCommitDiff();
        this.openFiles = revealFileTab(this.openFiles, path, line || 0, p => this.pinnedFiles.has(p));
        this.activeFilePath = path;
        if (pin) this.pinTab(path);
        this.edges.updateFile(path);
        if (this.edges.isMobile.isMobile) this.activeTab = 'editor';
    }

    pinTab = (path: string): void => {
        const next = new Set(this.pinnedFiles);
        next.add(path);
        this.pinnedFiles = next;
    };

    handleSelectTab = (path: string): void => {
        this.activeFilePath = path;
    };

    handleCloseTab = async (path: string): Promise<void> => {
        const idx = this.openFiles.findIndex(f => f.path === path);
        if (idx < 0) return;

        if (this.unsavedFiles.has(path)) {
            const fileName = baseName(path) || path;
            const confirmed = await dialogStore.showConfirm({
                title: t('explorer.shared.unsavedChanges'),
                message: t('explorer.shared.unsavedDiscardMessage', { file: fileName }),
                confirmText: t('explorer.shared.discard'),
                cancelText: t('explorer.shared.cancel'),
                variant: 'destructive',
            });
            if (!confirmed) return;
            // Without this the discarded edit comes back on the next open.
            clearEditorDraft(path).catch(() => {});
            clearEditorHistory(path).catch(() => {});
        }

        this.closeTab(path, idx);
    }

    private closeTab(path: string, idx: number): void {
        const wasActive = path === this.activeFilePath;
        this.dropTabs(new Set([path]));
        if (wasActive) this.activateTabAfter(this.openFiles, idx);
    }

    /** Remove these paths from the tabs, the unsaved set, and the pins. */
    private dropTabs(paths: Set<string>): void {
        const still = (file: string) => !paths.has(file);
        this.openFiles = this.openFiles.filter(f => still(f.path));
        this.unsavedFiles = new Set([...this.unsavedFiles].filter(still));
        this.pinnedFiles = new Set([...this.pinnedFiles].filter(still));
    }

    /** Activate the tab next to the one at `idx`, or none if nothing is left. */
    private activateTabAfter(next: OpenFileTab[], idx: number): void {
        if (next.length === 0) {
            this.activeFilePath = '';
            this.edges.updateFile(null);
            if (this.edges.isMobile.isMobile) this.activeTab = 'files';
            this.editorFullscreen = false;
            return;
        }
        // Adjacent tab, preferring the left neighbour.
        const newIdx = Math.min(idx, next.length - 1);
        this.activeFilePath = next[newIdx].path;
        this.edges.updateFile(this.activeFilePath);
    }

    // Close the tabs for files the listing deleted, with their drafts and undo history. A deleted
    // folder takes every tab under it.
    handleFilesDeleted = (paths: string[]): void => {
        const isGone = (file: string) => paths.some(p => isUnderPath(file, p));
        const gone = this.openFiles.filter(f => isGone(f.path));
        if (gone.length === 0) return;
        const firstIdx = this.openFiles.indexOf(gone[0]);
        const wasActive = isGone(this.activeFilePath);
        for (const tab of gone) {
            clearEditorDraft(tab.path).catch(() => {});
            clearEditorHistory(tab.path).catch(() => {});
        }
        this.dropTabs(new Set(gone.map(f => f.path)));
        if (wasActive) this.activateTabAfter(this.openFiles, firstIdx);
    };

    // ---- Save handshake ----

    /** Ask the active editor to save; EDITOR_SAVE_SETTLED frees the button. */
    handleEditorSave = (): void => {
        if (!this.activeFilePath || !this.unsavedFiles.has(this.activeFilePath)) return;
        if (!this.saveGuard.request(this.activeFilePath)) return;
        this.editorSaving = true;
        window.dispatchEvent(new CustomEvent('editor-save-request'));
        if (this.saveBackstop) clearTimeout(this.saveBackstop);
        this.saveBackstop = setTimeout(() => { this.saveGuard.expire(); this.editorSaving = false; }, SAVE_SETTLE_BACKSTOP_MS);
    };

    private handleSaveSettled = (e: Event): void => {
        if (!this.saveGuard.settled((e as CustomEvent<{ path?: string }>).detail?.path)) return;
        if (this.saveBackstop) { clearTimeout(this.saveBackstop); this.saveBackstop = null; }
        this.editorSaving = false;
    };

    // ---- Editor plumbing ----

    handleSymbolsUpdate = (newSymbols: LSPSymbol[]): void => {
        this.symbols = newSymbols;
    };

    handleEditorFocused = (focused: boolean): void => {
        this.editorFocused = focused;
    };

    handleHasChanges = (filePath: string, hasChanges: boolean): void => {
        const currentlyUnsaved = this.unsavedFiles.has(filePath);
        if (hasChanges === currentlyUnsaved) return;
        const next = new Set(this.unsavedFiles);
        if (hasChanges) next.add(filePath);
        else next.delete(filePath);
        this.unsavedFiles = next;
    };

    handleLinesSelected = (sel: { relativePath: string; startLine: number; lines: string[] }): void => {
        uiStore.createTaskLineSelection = { prompt: buildLineSelectionPrompt(sel) };
        uiStore.creatingChildOf = this.edges.lineSelectionParent();
        uiStore.showCreateTask = true;
    };

    handleEditFromChanges = (path: string, line?: number): void => {
        this.handleFileSelected(path, line);
        this.edges.setChangesOpen(false);
    };

    // ---- Find, tabs, fullscreen ----

    toggleFind = (): void => {
        this.showFind = !this.showFind;
    };

    closeFind = (): void => {
        this.showFind = false;
    };

    toggleEditorFullscreen = (): void => {
        this.editorFullscreen = !this.editorFullscreen;
    };

    /** Symbol clicks land in the editor, which on mobile means switching to its tab first. */
    revealSymbol = (line: number): void => {
        if (this.edges.isMobile.isMobile) {
            this.activeTab = 'editor';
            // The editor has to be visible before it can scroll.
            requestAnimationFrame(() => {
                window.dispatchEvent(new CustomEvent('jump-to-line', { detail: { line } }));
            });
            return;
        }
        window.dispatchEvent(new CustomEvent('jump-to-line', { detail: { line } }));
    }

    // ---- History and the commit-diff overlay ----

    loadCommits = async (): Promise<void> => {
        this.loadingCommits = true;
        this.commitOffset = 0;
        try {
            const resp = await this.edges.fetchLog(0, COMMIT_BATCH_SIZE);
            this.commits = resp.commits;
            this.commitOffset = resp.commits.length;
            this.originTip = resp.origin_tip || '';
            this.hasMoreCommits = resp.has_more || false;
        } catch (e) {
            console.error('Failed to load commits:', e);
            this.commits = [];
            this.originTip = '';
            this.hasMoreCommits = false;
        } finally {
            this.loadingCommits = false;
        }
    }

    loadMoreCommits = async (): Promise<void> => {
        if (this.loadingMoreCommits || !this.hasMoreCommits) return;
        this.loadingMoreCommits = true;
        try {
            const resp = await this.edges.fetchLog(this.commitOffset, COMMIT_BATCH_SIZE);
            this.commits = [...this.commits, ...resp.commits];
            this.commitOffset += resp.commits.length;
            this.hasMoreCommits = resp.has_more || false;
        } catch (e) {
            console.error('Failed to load more commits:', e);
        } finally {
            this.loadingMoreCommits = false;
        }
    }

    handleCommitSelected = async (commit: GitCommit, index: number): Promise<void> => {
        this.diffCommit = commit;
        this.diffCommitIndex = index;
        this.diffLoading = true;
        this.diffCommitFiles = [];
        if (this.edges.isMobile.isMobile) this.activeTab = 'editor';
        try {
            // The previous commit in the log is the base; the first parent when the log is
            // narrowed, and the empty tree for a root commit.
            let baseHash: string;
            if (index > 0) {
                baseHash = this.commits[index - 1].hash;
            } else if (commit.parents && commit.parents.length > 0) {
                baseHash = commit.parents[0];
            } else {
                baseHash = EMPTY_TREE;
            }
            this.diffCommitFiles = await this.edges.fetchCommitDiff(baseHash, commit.hash);
        } catch (e) {
            console.error('Failed to fetch commit diff:', e);
            this.diffCommitFiles = [];
        } finally {
            this.diffLoading = false;
        }
    }

    closeCommitDiff = (): void => {
        this.diffCommit = null;
        this.diffCommitIndex = -1;
        this.diffCommitFiles = [];
    };

    handleDiffNavigate = (direction: 'prev' | 'next'): void => {
        const newIndex = this.diffCommitIndex + (direction === 'next' ? -1 : 1);
        if (newIndex >= 0 && newIndex < this.commits.length) {
            void this.handleCommitSelected(this.commits[newIndex], newIndex);
        }
    };

    // ---- Keyboard ----

    /** Escape closes panels in order: this screen's own, then find, review, diff, the screen. */
    handleKeydown = (e: KeyboardEvent): void => {
        if (e.key === 'Escape') {
            // A modal (e.g. the floating line-task modal) owns Escape while open
            if (isAnyModalOpen()) return;
            if (this.edges.consumeEscape?.()) return;
            if (this.showFind) {
                this.showFind = false;
            } else if (this.edges.changesOpen()) {
                this.edges.setChangesOpen(false);
            } else if (this.diffCommit) {
                this.closeCommitDiff();
            } else if (!this.editorFocused) {
                this.edges.closeView();
            }
        }
        if ((e.ctrlKey || e.metaKey) && e.shiftKey && e.key === 'F') {
            e.preventDefault();
            this.toggleFind();
        }
        // Toggle-fullscreen shortcut (default Ctrl+Up): maximize/minimize the
        // editor when it is the focused area (file open, no commit diff).
        if (e.key === 'ArrowUp' && !e.repeat && !e.defaultPrevented && !this.diffCommit && this.openFiles.length > 0) {
            const key = effectiveToggleFullscreenKey(appStore.config);
            if (key && matchShortcut(key, e)) {
                const active = document.activeElement;
                const center = document.querySelector('.explorer-center-column');
                if (active instanceof HTMLElement && center?.contains(active)) {
                    e.preventDefault();
                    this.toggleEditorFullscreen();
                }
            }
        }
    };
}
