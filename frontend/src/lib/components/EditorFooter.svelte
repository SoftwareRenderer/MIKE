<script lang="ts">
    import { fetchLSPSandboxMemory } from '$lib/api/lsp';
    import { t } from '$lib/i18n';
    import { formatSandboxMemory, isSandboxMemoryWarning } from '$lib/utils/memory-stats';
    import type { SandboxMemoryStats } from '$lib/types';

    let { cursorLine, cursorCol, lineCount, charCount, onFind, onWrapToggle, lspReady, lspConnecting, lspError, lspLanguage, lspServerName, lspServerVersion, lspMessage, lspWorkspace, lspFile }: {
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
        // Absolute host paths identifying the LSP workspace + open file; used
        // to fetch the LSP sandbox's memory usage for the status tooltip.
        lspWorkspace?: string;
        lspFile?: string;
    } = $props();

    let lspTooltipVisible = $state(false);
    let lspSandboxStats = $state<SandboxMemoryStats | null>(null);
    let lspMemoryLoading = $state(false);
    // Monotonic fetch sequence: closing the tooltip (or reopening it) invalidates
    // any in-flight fetch so a stale result never lands in a (re)opened tooltip.
    let lspMemoryFetchSeq = 0;
    let showFileStats = $state(false);
    let wordWrap = $state(false);

    const lspStatusLabel = $derived(lspReady ? lspLanguage || 'LSP' : '-');

    // The memory row is only meaningful when the editor provides the LSP
    // workspace/file context (and the LSP footer section is rendered).
    const lspMemoryRowVisible = $derived(!!lspWorkspace && !!lspFile);

    async function toggleLspTooltip() {
        lspTooltipVisible = !lspTooltipVisible;
        if (lspTooltipVisible && lspMemoryRowVisible) {
            // One-shot fetch on open (no polling while open).
            const seq = ++lspMemoryFetchSeq;
            lspSandboxStats = null;
            lspMemoryLoading = true;
            const stats = await fetchLSPSandboxMemory(lspWorkspace!, lspFile!);
            if (seq === lspMemoryFetchSeq) {
                lspSandboxStats = stats;
                lspMemoryLoading = false;
            }
        } else {
            lspMemoryFetchSeq++;
            lspSandboxStats = null;
            lspMemoryLoading = false;
        }
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
                class:active={lspTooltipVisible}
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
                        <span class="lsp-tooltip-label">{t('lsp.provider')}</span>
                        <span class="lsp-tooltip-value">{lspServerName || '—'}{lspServerVersion ? ` ${lspServerVersion}` : ''}</span>
                    </div>
                    <div class="lsp-tooltip-row">
                        <span class="lsp-tooltip-label">{t('lsp.status')}</span>
                        <span class="lsp-tooltip-value" class:lsp-ready={lspReady} class:lsp-error={!!lspError}>
                            {#if lspError}{lspError}{:else if lspReady}{t('lsp.connected')}{:else}{t('lsp.connecting')}{/if}
                        </span>
                    </div>
                    {#if lspMemoryRowVisible}
                        <div class="lsp-tooltip-row">
                            <span class="lsp-tooltip-label">{t('lsp.sandboxMemory')}</span>
                            <span class="lsp-tooltip-value" class:lsp-error={isSandboxMemoryWarning(lspSandboxStats)}>
                                {lspMemoryLoading ? '…' : formatSandboxMemory(lspSandboxStats)}
                            </span>
                        </div>
                    {/if}
                    {#if lspMessage}
                        <div class="lsp-tooltip-row">
                            <span class="lsp-tooltip-label">{t('lsp.log')}</span>
                            <div class="lsp-tooltip-log">{lspMessage}</div>
                        </div>
                    {/if}
                </div>
            {/if}
        {/if}
        <button type="button" class="footer-btn" onclick={() => { showFileStats = !showFileStats }}>
            {#if showFileStats}
                {t('editor.fileStats', { lines: lineCount, chars: charCount })}
            {:else}
                {t('editor.cursorPosition', { line: cursorLine, col: cursorCol })}
            {/if}
        </button>
        <button type="button" class="footer-btn footer-toggle" class:active={wordWrap} title="Toggle word wrap" onclick={toggleWordWrap}>
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                <path d="M3 7h18"/>
                <path d="M3 17h12"/>
                <polyline points="15 10 18 10 18 13"/>
                <path d="M15 13h3"/>
            </svg>
            {t('editor.wrap')}
        </button>
    </div>
    <div class="footer-right">
        <button class="footer-btn" title="Find (Ctrl+F)" onclick={onFind}>
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round">
                <circle cx="11" cy="11" r="8"/>
                <line x1="21" y1="21" x2="16.65" y2="16.65"/>
            </svg>
            {t('editor.find')}
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
        animation: spin 0.6s linear infinite;
        opacity: 0.6;
    }
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
