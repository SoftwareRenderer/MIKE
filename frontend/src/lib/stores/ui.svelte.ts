// ─── UI Visibility State ───

export class UiStore {
    public showCreateTask = $state(false);
    public creatingChildOf = $state<string | null>(null);
    public showConfig = $state(false);
    public showTerminal = $state(false);
    public showStats = $state(false);
    public showCardDetail = $state(false);
    public showSandboxConfig = $state(false);
}
