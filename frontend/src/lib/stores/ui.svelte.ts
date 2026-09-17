import { loadAppSetting, saveAppSetting } from '$lib/storage';

/** IndexedDB app-settings key holding this browser's Review-notification choice. */
const NOTIFY_ON_REVIEW_KEY = 'notify-on-review';

// ─── UI Visibility State ───

export class UiStore {
    public showCreateTask = $state(false);
    public creatingChildOf = $state<string | null>(null);
    /** Prefilled prompt for the Create Task modal when opened from an editor
     *  gutter line selection (null = opened the usual way). */
    public createTaskLineSelection = $state<{ prompt: string } | null>(null);
    /** Floating task view (Chat only) opened after a gutter line-selection
     *  task is created. Session-only — no URL change. The stashed task covers
     *  the gap before the board WS update lands in the task list. */
    public floatingTask = $state<{ projectId: string; taskId: string; task?: any } | null>(null);
    public showConfig = $state(false);
    public showTerminal = $state(false);
    public showStats = $state(false);
    public showCardDetail = $state(false);
    public showSandboxConfig = $state(false);
    /** Which project's board is showing its Review column. Written only by `Board.svelte`. */
    public reviewColumnInViewOf = $state<string | null>(null);
    /** Whether this browser asks for a notification when a task enters Review. Per browser because
     *  permission and delivery are per browser — the server never sees it (docs/notifications.md). */
    public notifyOnReview = $state(false);

    constructor() {
        loadAppSetting(NOTIFY_ON_REVIEW_KEY, false).then((v: unknown) => {
            this.notifyOnReview = v === true;
        }).catch(() => {});
    }

    /** Write this browser's choice back to its own storage. */
    persistNotifyOnReview(): void {
        saveAppSetting(NOTIFY_ON_REVIEW_KEY, this.notifyOnReview).catch(() => {});
    }
}
