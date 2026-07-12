<script lang="ts">
    let { value, onChange }: { value: string; onChange: (mode: string) => void } = $props();

    const modes = [
        { value: 'act', label: 'Act', icon: 'check' },
        { value: 'plan', label: 'Plan', icon: 'list' },
        { value: 'ideate', label: 'Ideate', icon: 'bulb' },
    ];

    let open = $state(false);
    let dropdownRef: HTMLDivElement | null = null;
    let selected = $derived(modes.find(m => m.value === value) || modes[0]);

    $effect(() => {
        function handleClickOutside(e: MouseEvent) {
            if (dropdownRef && !dropdownRef.contains(e.target as Node)) {
                open = false;
            }
        }
        document.addEventListener('mousedown', handleClickOutside);
        return () => document.removeEventListener('mousedown', handleClickOutside);
    });

    function toggle() {
        open = !open;
    }

    function select(mode: string) {
        onChange(mode);
        open = false;
    }

    function handleKeydown(e: KeyboardEvent) {
        if (e.key === 'Escape') {
            open = false;
        }
    }

    function renderIcon(icon: string, active: boolean) {
        const cls = active ? 'icon-active' : 'icon';
        if (icon === 'check') {
            return `<svg class="${cls}" width="14" height="14" viewBox="0 0 16 16" fill="none"><path d="M3 8l4 4L13 4" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"/></svg>`;
        }
        if (icon === 'list') {
            return `<svg class="${cls}" width="14" height="14" viewBox="0 0 16 16" fill="none"><path d="M2 4h12M2 8h12M2 12h12" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/></svg>`;
        }
        if (icon === 'bulb') {
            return `<svg class="${cls}" width="14" height="14" viewBox="0 0 16 16" fill="none"><path d="M5 6a3 3 0 1 1 6 0c0 2-1.5 3-1.5 3v1h-3v-1s-1.5-1-1.5-3Z" stroke="currentColor" stroke-width="1.5" stroke-linejoin="round"/><path d="M6.5 10h3v1h-3Z" stroke="currentColor" stroke-width="1.5" stroke-linejoin="round"/><path d="M6.5 12h3" stroke="currentColor" stroke-width="1.5" stroke-linecap="round"/></svg>`;
        }
        return '';
    }
</script>

<div class="mode-select" bind:this={dropdownRef} onkeydown={handleKeydown}>
    <button class="mode-btn" class:open={open} onclick={toggle} aria-label="Operating mode" aria-expanded={open}>
        <span class="mode-icon">{@html renderIcon(selected.icon, true)}</span>
        <span class="mode-label">{selected.label}</span>
        <svg class="chevron" width="10" height="10" viewBox="0 0 10 10" fill="none">
            <path d="M2.5 4L5 6.5L7.5 4" stroke="currentColor" stroke-width="1.2" stroke-linecap="round" stroke-linejoin="round"/>
        </svg>
    </button>
    {#if open}
        <div class="mode-dropdown" role="listbox">
            {#each modes as mode}
                <button
                    class="mode-option"
                    class:selected={mode.value === value}
                    role="option"
                    aria-selected={mode.value === value}
                    onclick={() => select(mode.value)}
                >
                    <span class="mode-icon">{@html renderIcon(mode.icon, mode.value === value)}</span>
                    <span>{mode.label}</span>
                </button>
            {/each}
        </div>
    {/if}
</div>

<style>
    .mode-select {
        position: relative;
        display: inline-block;
    }
    .mode-btn {
        display: inline-flex;
        align-items: center;
        gap: 6px;
        padding: 5px 10px;
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
        background: var(--color-surface-2);
        color: var(--color-text-primary);
        font-size: 0.8125rem;
        font-weight: 500;
        cursor: pointer;
        font-family: inherit;
        white-space: nowrap;
        transition: border-color 0.15s;
    }
    .mode-btn:hover {
        border-color: var(--color-accent);
    }
    .mode-btn.open {
        border-color: var(--color-accent);
    }
    .mode-label {
        line-height: 1;
    }
    .chevron {
        margin-left: 2px;
        transition: transform 0.15s;
    }
    .mode-btn.open .chevron {
        transform: rotate(180deg);
    }
    .mode-icon {
        line-height: 1;
        display: flex;
        align-items: center;
    }
    .mode-icon .icon {
        color: var(--color-text-secondary);
    }
    .mode-icon .icon-active {
        color: var(--color-accent);
    }
    .mode-dropdown {
        position: absolute;
        top: calc(100% + 4px);
        right: 0;
        min-width: 140px;
        background: var(--color-surface-1);
        border: 1px solid var(--color-border);
        border-radius: var(--radius-sm);
        box-shadow: 0 4px 12px rgba(0,0,0,0.15);
        padding: 4px;
        z-index: 50;
    }
    .mode-option {
        display: flex;
        align-items: center;
        gap: 8px;
        width: 100%;
        padding: 6px 10px;
        border: none;
        border-radius: 3px;
        background: transparent;
        color: var(--color-text-primary);
        font-size: 0.8125rem;
        font-weight: 500;
        cursor: pointer;
        font-family: inherit;
        text-align: left;
    }
    .mode-option:hover {
        background: var(--color-surface-3);
    }
    .mode-option.selected {
        color: var(--color-accent);
    }
</style>
