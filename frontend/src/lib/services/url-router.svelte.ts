/**
 * What the address bar says, as app state. Both directions live here: `read()` adopts the address bar
 * (boot and popstate) and `write()` makes a navigation (the shell's only `history.pushState`/`replaceState`
 * call). `App.svelte` holds no param of its own any more; the reader left outside this module is
 * `Chat/TaskChat.svelte`'s lookup of the `thread` it owns.
 *
 * The field names are the URL's own: `?project=&task=&view=&file=&thread=`. Param order is canonical —
 * project, task, view, file, thread — and `$lib/utils/app-link.ts` builds the hrefs that the link-like
 * rows carry in that same order, so a browser-generated link lands on the screen a click reaches
 * (`docs/link-rows.md`).
 *
 * Which screen a `view` names is mirrored here too (`terminalOnly`, `explorerOpen`, `taskExplorerOpen`,
 * `changesOpen`), because the URL is what decides it on boot and after Back. `write()` does not change
 * those flags — a project switch remounts the explorer behind an unchanged URL, and that has to stay
 * possible — so a caller whose navigation opens a screen calls `applyView()` after `write()`.
 */
import { TERMINAL_VIEW, asksForTerminal, fileWithinRoots } from '$lib/utils/app-link';

/** The screens `view` names besides `terminal`. The Review Changes panel is an explorer with its panel
 *  open, which is why each explorer has two names. */
const PROJECT_EXPLORER_VIEW = 'explorer';
const PROJECT_REVIEW_VIEW = 'review';
const TASK_EXPLORER_VIEW = 'task-explorer';
const TASK_REVIEW_VIEW = 'task-review';

/** How long the external-change watcher waits after one change before looking again. A change the poll
 *  reports is acted on once, however fast the address bar kept moving. */
const SEARCH_THROTTLE_MS = 250;

/** The address bar as one flat record. An empty string means the param is absent. */
interface UrlState {
    project: string;
    task: string;
    view: string;
    file: string;
    thread: string;
}

/** Read every param the app owns out of a `location.search`. */
function parseSearch(search: string): UrlState {
    const params = new URLSearchParams(search);
    return {
        project: params.get('project') ?? '',
        task: params.get('task') ?? '',
        view: params.get('view') ?? '',
        file: params.get('file') ?? '',
        thread: params.get('thread') ?? '',
    };
}

/** Write the record back as a query string, in the canonical order. `file` keeps `/` literal, which is
 *  what tells the reader to list a directory rather than open a file. */
function buildSearch(state: UrlState): string {
    const parts: string[] = [];
    if (state.project) parts.push('project=' + encodeURIComponent(state.project));
    if (state.task) parts.push('task=' + encodeURIComponent(state.task));
    if (state.view) parts.push('view=' + encodeURIComponent(state.view));
    if (state.file) parts.push('file=' + state.file);
    if (state.thread) parts.push('thread=' + encodeURIComponent(state.thread));
    return parts.length ? '?' + parts.join('&') : '';
}

/** The `view` values that mean the Review Changes panel is open. */
function isChangesView(view: string): boolean {
    return view === PROJECT_REVIEW_VIEW || view === TASK_REVIEW_VIEW;
}

/** A navigation request. `project` and `task` are the destination: omitted means cleared. `view` and
 *  `file` are the surroundings: omitted means keep what the address bar says, `null` means clear. */
interface UrlWrite {
    project?: string | null;
    task?: string | null;
    view?: string | null;
    file?: string | null;
}

class UrlRouter {
    /** The address bar, as this module last read or wrote it. */
    project = $state('');
    task = $state('');
    view = $state('');
    file = $state('');
    thread = $state('');

    /** The screen the `view` param named at boot. `applyView()` owns it from the first navigation on. */
    terminalOnly = $state(asksForTerminal(location.search));
    changesOpen = $state(isChangesView(parseSearch(location.search).view));
    explorerOpen = $state(false);
    taskExplorerOpen = $state(false);

    /** The search this module last rendered from, and when it reported a change. See `refresh()`. */
    private seenSearch = location.search;
    private lastChangeAt = 0;

    /** Adopt the address bar. Returns what it says, so a caller can act on the params it names. */
    read(): UrlState {
        const state = parseSearch(location.search);
        this.project = state.project;
        this.task = state.task;
        this.view = state.view;
        this.file = state.file;
        this.thread = state.thread;
        return state;
    }

    /** Adopt the address bar if something outside this module moved it — a card click, a thread switch,
     *  `page.goto()`. Returns whether it moved; two reports are never closer than the throttle window. */
    refresh(): boolean {
        const now = Date.now();
        if (now - this.lastChangeAt < SEARCH_THROTTLE_MS) return false;
        if (location.search === this.seenSearch) return false;
        this.seenSearch = location.search;
        this.lastChangeAt = now;
        this.read();
        return true;
    }

    /** Call this after rendering a URL the module adopted outside `write()`, so the next `refresh()`
     *  reports only what arrives after now. */
    markSeen(): void {
        this.seenSearch = location.search;
        this.lastChangeAt = Date.now();
    }

    /**
     * Make the address bar say this navigation, and keep the state in step with what was written.
     *
     * The preserved params are read from the live address bar rather than from this module's fields:
     * `Board.svelte` and `Chat/TaskChat.svelte` push state of their own (a card's `task`, a chat's
     * `thread`) and this module only learns of it at the next `read()`/`refresh()`, so a navigation
     * must not drop what they just wrote.
     */
    write(params: UrlWrite, clearThread = false, push = false): void {
        const current = new URLSearchParams(location.search);
        const state: UrlState = {
            project: params.project ?? '',
            task: params.task ?? '',
            view: (params.view !== undefined ? params.view : current.get('view')) ?? '',
            file: (params.file !== undefined ? params.file : current.get('file')) ?? '',
            thread: clearThread ? '' : current.get('thread') ?? '',
        };
        // The Review Changes panel state follows a `view` that was named, whether or not it moved.
        if (params.view !== undefined) this.changesOpen = isChangesView(state.view);
        const search = buildSearch(state);
        if (location.search !== search) {
            if (push) {
                history.pushState(null, '', location.pathname + search);
            } else {
                history.replaceState(null, '', location.pathname + search);
            }
        }
        this.seenSearch = search;
        this.project = state.project;
        this.task = state.task;
        this.view = state.view;
        this.file = state.file;
        this.thread = state.thread;
    }

    /**
     * Show the screen the `view` param names. Returns whether it named a screen of its own — the task
     * detail opens only when it named none. The terminal screen replaces whatever was open without
     * closing it in state, so leaving it restores the screen the next `view` names.
     */
    applyView(): boolean {
        if (this.view === TERMINAL_VIEW) {
            this.terminalOnly = true;
            return true;
        }
        this.terminalOnly = false;
        if (this.view === PROJECT_EXPLORER_VIEW || this.view === PROJECT_REVIEW_VIEW) {
            this.explorerOpen = true;
            this.taskExplorerOpen = false;
            this.changesOpen = this.view === PROJECT_REVIEW_VIEW;
        } else if (this.view === TASK_EXPLORER_VIEW || this.view === TASK_REVIEW_VIEW) {
            this.explorerOpen = false;
            this.taskExplorerOpen = true;
            this.changesOpen = this.view === TASK_REVIEW_VIEW;
        } else {
            this.explorerOpen = false;
            this.taskExplorerOpen = false;
            this.changesOpen = false;
        }
        return this.explorerOpen || this.taskExplorerOpen;
    }

    /** The `view` that names the screen on screen, so a navigation that keeps the screen can keep
     *  saying it. Answers empty when the screen is the board or a task detail — the `view` param then
     *  belongs to nobody and a `write` with this answer drops it. */
    currentView(): string {
        if (this.terminalOnly) return TERMINAL_VIEW;
        if (this.explorerOpen) return this.changesOpen ? PROJECT_REVIEW_VIEW : PROJECT_EXPLORER_VIEW;
        if (this.taskExplorerOpen) return this.changesOpen ? TASK_REVIEW_VIEW : TASK_EXPLORER_VIEW;
        return '';
    }

    /** Unmount both explorers and re-arm them on the next microtask: another project's explorer is a
     *  new component even though its `view` says the same thing. Returns whether one was mounted. */
    remountExplorers(): boolean {
        const { explorerOpen, taskExplorerOpen } = this;
        if (!explorerOpen && !taskExplorerOpen) return false;
        this.explorerOpen = false;
        this.taskExplorerOpen = false;
        queueMicrotask(() => {
            this.explorerOpen = explorerOpen;
            this.taskExplorerOpen = taskExplorerOpen;
        });
        return true;
    }

    /**
     * Take the `file` param a navigation just adopted, or reject it: it is honoured only when it sits
     * inside an explorer this screen can open (`explorerPathRoots` says which those are).
     */
    acceptFile(file: string, roots: string[]): void {
        this.file = fileWithinRoots(file, roots) ? file : '';
    }

    /** Put the task's explorer in the shared slot — the task view and both explorers occupy one place.
     *  Returns whether this call is what opened it; a definition jump waits on that mount. */
    openTaskExplorer(projectId: string, taskId: string): boolean {
        const wasOpen = this.taskExplorerOpen;
        this.write({ project: projectId, task: taskId, view: TASK_EXPLORER_VIEW }, false, true);
        this.applyView();
        return !wasOpen;
    }

    /** Open the project explorer and mirror it in the URL. */
    openProjectExplorer(projectId: string): void {
        this.write({ project: projectId, view: PROJECT_EXPLORER_VIEW }, false, true);
        this.applyView();
    }

    /** The URL has been naming the project explorer while the board was on screen. Show it now that
     *  there is a branch to list; any other `view` is left alone. */
    restoreProjectExplorer(): void {
        if (this.view !== PROJECT_EXPLORER_VIEW && this.view !== PROJECT_REVIEW_VIEW) return;
        this.applyView();
    }
}

/** One address bar per page, so one router. */
export const urlRouter = new UrlRouter();
