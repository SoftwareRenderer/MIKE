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
}
