<script lang="ts">
    let { cursorLine, cursorCol, lineCount, charCount, hasChanges, onFind, onSave, saving }: {
        cursorLine: number;
        cursorCol: number;
        lineCount: number;
        charCount: number;
        hasChanges: boolean;
        onFind: () => void;
        onSave?: () => void;
        saving?: boolean;
    } = $props();
</script>

<div class="editor-footer">
    <div class="footer-left">
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
