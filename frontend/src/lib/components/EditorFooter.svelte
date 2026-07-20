<script lang="ts">
    let { cursorLine, cursorCol, lineCount, charCount, hasChanges, onFind, onSave, saving, lspReady, lspConnecting, lspError, lspLanguage, lspServerName, lspServerVersion }: {
        cursorLine: number;
        cursorCol: number;
        lineCount: number;
        charCount: number;
        hasChanges: boolean;
        onFind: () => void;
        onSave?: () => void;
        saving?: boolean;
        lspReady?: boolean;
        lspConnecting?: boolean;
        lspError?: string;
        lspLanguage?: string;
        lspServerName?: string;
        lspServerVersion?: string;
    } = $props();

    let lspTooltipVisible = $state(false);
    // svelte-ignore non_reactive_update — DOM ref via bind:this
    let lspStatusEl: HTMLButtonElement | null = null;

    const lspStatusLabel = $derived(lspReady ? lspLanguage || 'LSP' : '-');

    function toggleLspTooltip() {
        lspTooltipVisible = !lspTooltipVisible;
    }
</script>

<div class="editor-footer">
    <div class="footer-left">
        {#if lspReady !== undefined}
            <button
                type="button"
                class="lsp-status"
                bind:this={lspStatusEl}
                onclick={toggleLspTooltip}
                aria-label="LSP status"
            >
                {#if lspConnecting}
                    <span class="lsp-spinner"></span>
                {/if}
                {lspStatusLabel}
            </button>
            {#if lspTooltipVisible}
                <!-- svelte-ignore a11y_click_events_have_key_events -->
                <!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
                <div class="lsp-tooltip" role="tooltip" onclick={(e) => e.stopPropagation()}>
                    <div class="lsp-tooltip-row">
                        <span class="lsp-tooltip-label">LSP Provider</span>
                        <span class="lsp-tooltip-value">{lspServerName || '—'}{lspServerVersion ? ` ${lspServerVersion}` : ''}</span>
                    </div>
                    <div class="lsp-tooltip-row">
                        <span class="lsp-tooltip-label">Status</span>
                        <span class="lsp-tooltip-value" class:lsp-ready={lspReady} class:lsp-error={!!lspError}>
                            {#if lspError}{lspError}{:else if lspReady}Connected{:else}Connecting...{/if}
                        </span>
                    </div>
                </div>
            {/if}
        {/if}
        <span class="cursor-info">Ln {cursorLine}, Col {cursorCol}</span>
    </div>
    <div class="footer-right">
        {#if onSave}
            <button
                class="footer-save-btn"
                class:active={hasChanges}
                class:saving={saving}
                disabled={!hasChanges || saving}
                onclick={onSave}
                title="Save (Ctrl+S)"
            >
                {#if saving}Saving...{:else}Save{/if}
            </button>
        {/if}
        <span class="line-info">{lineCount} lines · {charCount} chars</span>
        <button class="footer-find-btn" title="Find (Ctrl+F)" onclick={onFind}>
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                <circle cx="11" cy="11" r="8"/>
                <line x1="21" y1="21" x2="16.65" y2="16.65"/>
            </svg>
            Find
        </button>
    </div>
</div>

<style>
    .editor-footer {
        display: flex;
        align-items: center;
        justify-content: space-between;
        gap: 12px;
        padding: 4px 10px;
        border-top: 1px solid var(--color-border);
        flex-shrink: 0;
        font-size: 12px;
        color: var(--color-text-tertiary);
        background: var(--color-surface-2);
        position: relative;
    }
    .footer-left {
        display: flex;
        align-items: center;
        gap: 12px;
    }
    .footer-right {
        display: flex;
        align-items: center;
        gap: 12px;
    }
    .cursor-info {
        font-family: var(--font-mono);
    }
    .lsp-status {
        font-family: var(--font-mono);
        cursor: pointer;
        background: none;
        border: none;
        padding: 0;
        font-size: inherit;
        color: inherit;
        display: inline-flex;
        align-items: center;
        gap: 4px;
    }
    .lsp-spinner {
        display: inline-block;
        width: 10px;
        height: 10px;
        border: 2px solid currentColor;
        border-top-color: transparent;
        border-radius: 50%;
        animation: lsp-spin 0.6s linear infinite;
        opacity: 0.6;
    }
    @keyframes lsp-spin { to { transform: rotate(360deg); } }
    .lsp-tooltip {
        position: absolute;
        bottom: calc(100% + 4px);
        left: 0;
        background: var(--color-surface-3);
        border: 1px solid var(--color-border);
        border-radius: 6px;
        padding: 6px 12px;
        z-index: 100;
        box-shadow: 0 4px 12px rgba(0, 0, 0, 0.3);
        font-size: 12px;
        min-width: 180px;
    }
    .lsp-tooltip-row {
        display: flex;
        justify-content: space-between;
        gap: 16px;
        padding: 2px 0;
    }
    .lsp-tooltip-row:not(:last-child) {
        border-bottom: 1px solid var(--color-border);
    }
    .lsp-tooltip-label {
        color: var(--color-text-secondary);
    }
    .lsp-tooltip-value {
        color: var(--color-text-primary);
        font-family: var(--font-mono);
        text-align: right;
    }
    .lsp-tooltip-value.lsp-ready {
        color: var(--color-status-green);
    }
    .lsp-tooltip-value.lsp-error {
        color: var(--color-status-red);
    }
    .line-info {
        font-family: var(--font-mono);
    }
    .footer-find-btn {
        display: flex;
        align-items: center;
        gap: 4px;
        padding: 2px 8px;
        border: none;
        border-radius: 4px;
        background: var(--color-surface-3);
        color: var(--color-text-secondary);
        cursor: pointer;
        font-size: 12px;
        font-family: inherit;
        flex-shrink: 0;
    }
    .footer-find-btn:hover {
        background: var(--color-surface-4);
        color: var(--color-text-primary);
    }
    .footer-save-btn {
        display: flex;
        align-items: center;
        gap: 4px;
        padding: 2px 10px;
        border: none;
        border-radius: 4px;
        background: var(--color-surface-3);
        color: var(--color-text-secondary);
        cursor: pointer;
        font-size: 12px;
        font-family: inherit;
        font-weight: 500;
        flex-shrink: 0;
        transition: background 0.1s, color 0.1s;
    }
    .footer-save-btn.active {
        background: var(--color-accent-dark);
        color: white;
    }
    .footer-save-btn:hover:not(:disabled) {
        opacity: 0.9;
    }
    .footer-save-btn:disabled {
        opacity: 0.4;
        cursor: not-allowed;
    }
    .footer-save-btn.saving {
        color: var(--color-status-orange);
    }
</style>
