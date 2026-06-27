<script lang="ts">
    let { symbols, onSymbolClick }: {
        symbols: Array<{ name: string; kind: string; line: number; indent: number }>;
        onSymbolClick: (line: number) => void;
    } = $props();

    // Icon mapping for symbol kinds
    function getSymbolIcon(kind: string): string {
        switch (kind) {
            case 'function': return 'ƒ';
            case 'class': return 'C';
            case 'constant': return 'K';
            case 'property': return 'P';
            case 'variable': return 'V';
            default: return '•';
        }
    }

    function getSymbolColor(kind: string): string {
        switch (kind) {
            case 'function': return '#D2A8FF';
            case 'class': return '#FFA657';
            case 'constant': return '#79C0FF';
            case 'property': return '#79C0FF';
            case 'variable': return '#FFA657';
            default: return '#AEB2B8';
        }
    }
</script>

<div class="symbol-outline">
    <div class="symbol-header">
        <span class="symbol-label">Symbols</span>
        {#if symbols.length > 0}
            <span class="symbol-count">{symbols.length}</span>
        {/if}
    </div>

    <div class="symbol-list" role="tree" aria-label="Symbol outline">
        {#if symbols.length === 0}
            <div class="symbol-empty">No symbols</div>
        {:else}
            {#each symbols as symbol}
                <button
                    class="symbol-item"
                    onclick={() => onSymbolClick(symbol.line)}
                    role="treeitem"
                    title={`Line ${symbol.line}: ${symbol.name}`}
                >
                    <span
                        class="symbol-icon"
                        style="color: {getSymbolColor(symbol.kind)}"
                    >
                        {getSymbolIcon(symbol.kind)}
                    </span>
                    <span class="symbol-name" style="padding-left: {symbol.indent * 8}px">
                        {symbol.name}
                    </span>
                    <span class="symbol-line">{symbol.line}</span>
                </button>
            {/each}
        {/if}
    </div>
</div>

<style>
    .symbol-outline {
        display: flex;
        flex-direction: column;
        height: 100%;
        overflow: hidden;
    }

    .symbol-header {
        display: flex;
        align-items: center;
        justify-content: space-between;
        padding: 6px 10px;
        border-bottom: 1px solid var(--color-border);
        flex-shrink: 0;
    }

    .symbol-label {
        font-size: 0.6875rem;
        font-weight: 600;
        color: var(--color-text-secondary);
        text-transform: uppercase;
        letter-spacing: 0.05em;
    }

    .symbol-count {
        font-size: 0.625rem;
        color: var(--color-text-tertiary);
        background: var(--color-surface-3);
        padding: 1px 5px;
        border-radius: 8px;
    }

    .symbol-list {
        flex: 1;
        overflow-y: auto;
        overflow-x: hidden;
        padding: 2px 0;
    }

    .symbol-item {
        display: flex;
        align-items: center;
        gap: 4px;
        width: 100%;
        padding: 2px 8px;
        border: none;
        background: transparent;
        color: var(--color-text-primary);
        font-size: 0.6875rem;
        text-align: left;
        cursor: pointer;
    }
    .symbol-item:hover {
        background: var(--color-surface-2);
        opacity: 1;
    }

    .symbol-icon {
        flex-shrink: 0;
        width: 14px;
        text-align: center;
        font-size: 0.625rem;
        font-weight: 700;
        font-family: var(--font-mono);
        line-height: 1;
    }

    .symbol-name {
        flex: 1;
        min-width: 0;
        white-space: nowrap;
        overflow: hidden;
        text-overflow: ellipsis;
        font-family: var(--font-mono);
    }

    .symbol-line {
        flex-shrink: 0;
        font-size: 0.625rem;
        color: var(--color-text-tertiary);
        opacity: 0.5;
        font-family: var(--font-mono);
    }

    .symbol-empty {
        padding: 8px 10px;
        color: var(--color-text-tertiary);
        font-size: 0.75rem;
    }
</style>
