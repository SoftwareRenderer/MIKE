<script lang="ts">
    let { cursorLine, cursorCol, lineCount, charCount, onFind, onWrapToggle, lspReady, lspConnecting, lspError, lspLanguage, lspServerName, lspServerVersion, lspMessage }: {
        cursorLine: number;
        cursorCol: number;
        lineCount: number;
        charCount: number;
        onFind: () => void;
        onWrapToggle: (enabled: boolean) => void;
        lspReady?: boolean;
        lspConnecting?: boolean;
        lspError?: string;
        lspLanguage?: string;
        lspServerName?: string;
        lspServerVersion?: string;
        lspMessage?: string;
    } = $props();

    let lspTooltipVisible = $state(false);
    let showFileStats = $state(false);
    let wordWrap = $state(false);
    // svelte-ignore non_reactive_update — DOM ref via bind:this
    let lspStatusEl: HTMLButtonElement | null = null;

    const lspStatusLabel = $derived(lspReady ? lspLanguage || 'LSP' : '-');

    function toggleLspTooltip() {
        lspTooltipVisible = !lspTooltipVisible;
    }

    function toggleWordWrap() {
        wordWrap = !wordWrap;
        onWrapToggle(wordWrap);
    }
</script>

<div class="editor-footer">
    <div class="footer-left">
        {#if lspReady !== undefined}
            <button
                type="button"
                class="footer-btn footer-toggle"
                class:active={lspReady}
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
                    {#if lspMessage}
                        <div class="lsp-tooltip-row">
                            <span class="lsp-tooltip-label">LSP Log</span>
                            <div class="lsp-tooltip-log">{lspMessage}</div>
                        </div>
                    {/if}
                </div>
            {/if}
        {/if}
        <button type="button" class="footer-btn" onclick={() => { showFileStats = !showFileStats }}>
            {#if showFileStats}
                {lineCount} lines · {charCount} chars
            {:else}
                Ln {cursorLine}, Col {cursorCol}
            {/if}
        </button>
        <button type="button" class="footer-btn footer-toggle" class:active={wordWrap} title="Toggle word wrap" onclick={toggleWordWrap}>
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                <path d="M3 7h18"/>
                <path d="M3 17h12"/>
                <polyline points="15 10 18 10 18 13"/>
                <path d="M15 13h3"/>
            </svg>
            Wrap
        </button>
    </div>
    <div class="footer-right">
        <button class="footer-btn" title="Find (Ctrl+F)" onclick={onFind}>
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
    .lsp-tooltip-log {
        max-width: 280px;
        max-height: 120px;
        overflow-y: auto;
        text-align: left;
        font-family: var(--font-mono);
        font-size: 11px;
        color: var(--color-text-secondary);
        white-space: pre-wrap;
        word-break: break-word;
        line-height: 1.4;
    }
    .footer-btn,
    :global(.ext-footer-btn) {
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
        font-family: var(--font-mono);
        flex-shrink: 0;
    }
    .footer-btn:hover,
    :global(.ext-footer-btn:hover) {
        background: var(--color-surface-4);
        color: var(--color-text-primary);
    }
    .footer-btn.active,
    :global(.ext-footer-btn.active) {
        background: var(--color-surface-4);
        color: var(--color-text-primary);
    }
    .footer-btn.active:hover,
    :global(.ext-footer-btn.active:hover) {
        background: #484848;
        color: var(--color-text-primary);
    }
    .footer-btn.footer-toggle.active,
    :global(.ext-footer-btn.footer-toggle.active) {
        outline: 1px solid var(--color-accent-light);
        outline-offset: -1px;
    }
</style>
